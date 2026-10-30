package secret_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"ai-router/internal/secret"
)

func mustEnvelope(t *testing.T, masterKey []byte) *secret.Envelope {
	t.Helper()
	envelope, err := secret.NewEnvelope(masterKey)
	if err != nil {
		t.Fatalf("NewEnvelope() error = %v", err)
	}
	return envelope
}

func splitEnvelope(t *testing.T, encoded string) (string, []byte, []byte) {
	t.Helper()
	parts := strings.Split(encoded, ":")
	if len(parts) != 3 {
		t.Fatalf("信封字段数 = %d, want 3 (%q)", len(parts), encoded)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("nonce 不是 base64url: %v", err)
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("ciphertext 不是 base64url: %v", err)
	}
	return parts[0], nonce, ciphertext
}

func assertErrorHides(t *testing.T, err error, secretValue string) {
	t.Helper()
	if err == nil {
		t.Fatal("期望返回错误，得到 nil")
	}
	if secretValue != "" && strings.Contains(err.Error(), secretValue) {
		t.Fatalf("错误信息回显了敏感值 %q: %v", secretValue, err)
	}
}

func TestParseMasterKey(t *testing.T) {
	raw := bytes.Repeat([]byte("0123456789abcdef"), 2) // 32 字节
	tests := []struct {
		name    string
		encoded string
		want    []byte
		wantErr bool
	}{
		{
			name:    "标准 base64 带 padding",
			encoded: base64.StdEncoding.EncodeToString(raw),
			want:    raw,
		},
		{
			name:    "标准 base64 无 padding",
			encoded: base64.RawStdEncoding.EncodeToString(raw),
			want:    raw,
		},
		{name: "空字符串", encoded: "", wantErr: true},
		{name: "非 base64", encoded: "not-base64-!@#$%^&*()", wantErr: true},
		{
			name:    "31 字节",
			encoded: base64.StdEncoding.EncodeToString(raw[:31]),
			wantErr: true,
		},
		{
			name:    "33 字节",
			encoded: base64.StdEncoding.EncodeToString(append(append([]byte(nil), raw...), 'x')),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := secret.ParseMasterKey(tt.encoded)
			if tt.wantErr {
				if !errors.Is(err, secret.ErrInvalidMasterKey) {
					t.Fatalf("ParseMasterKey() error = %v, want ErrInvalidMasterKey", err)
				}
				if got != nil {
					t.Fatalf("失败时返回了非 nil 密钥: %x", got)
				}
				assertErrorHides(t, err, tt.encoded)
				return
			}
			if err != nil {
				t.Fatalf("ParseMasterKey() error = %v", err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("ParseMasterKey() = %x, want %x", got, tt.want)
			}
		})
	}
}

func TestNewEnvelopeRejectsInvalidKeyLength(t *testing.T) {
	for _, size := range []int{0, 1, 16, 31, 33} {
		key := bytes.Repeat([]byte("K"), size)
		envelope, err := secret.NewEnvelope(key)
		if !errors.Is(err, secret.ErrInvalidMasterKey) {
			t.Errorf("NewEnvelope(len=%d) error = %v, want ErrInvalidMasterKey", size, err)
		}
		if envelope != nil {
			t.Errorf("NewEnvelope(len=%d) 返回了非 nil 信封", size)
		}
		if size > 0 {
			assertErrorHides(t, err, string(key))
		}
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	masterKey := bytes.Repeat([]byte{0x42}, secret.MasterKeySize)
	envelope := mustEnvelope(t, masterKey)
	plaintext := "sk-upstream-1234567890abcdef"
	purpose := secret.PurposeChannelKey

	encoded, err := envelope.EncryptString(plaintext, purpose)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}

	version, nonce, ciphertext := splitEnvelope(t, encoded)
	if version != secret.EnvelopeVersion {
		t.Errorf("版本 = %q, want %q", version, secret.EnvelopeVersion)
	}
	if len(nonce) != 12 {
		t.Errorf("nonce 长度 = %d, want 12", len(nonce))
	}
	if want := len(plaintext) + 16; len(ciphertext) != want {
		t.Errorf("密文长度 = %d, want %d（明文 + GCM tag）", len(ciphertext), want)
	}
	if strings.ContainsAny(encoded, "+/=") {
		t.Errorf("信封包含非 base64url 字符: %q", encoded)
	}

	decrypted, err := envelope.DecryptString(encoded, purpose)
	if err != nil {
		t.Fatalf("DecryptString() error = %v", err)
	}
	if decrypted != plaintext {
		t.Errorf("DecryptString() = %q, want %q", decrypted, plaintext)
	}

	// 字节 API 同样可用，并且空明文也能往返。
	rawEnvelope, err := envelope.Encrypt([]byte(plaintext), purpose)
	if err != nil {
		t.Fatalf("Encrypt() error = %v", err)
	}
	raw, err := envelope.Decrypt(rawEnvelope, purpose)
	if err != nil {
		t.Fatalf("Decrypt() error = %v", err)
	}
	if string(raw) != plaintext {
		t.Errorf("Decrypt() = %q, want %q", raw, plaintext)
	}

	emptyEnvelope, err := envelope.Encrypt(nil, purpose)
	if err != nil {
		t.Fatalf("Encrypt(nil) error = %v", err)
	}
	empty, err := envelope.Decrypt(emptyEnvelope, purpose)
	if err != nil {
		t.Fatalf("Decrypt(empty) error = %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("空明文解密结果长度 = %d, want 0", len(empty))
	}
}

func TestEncryptUsesFreshNonce(t *testing.T) {
	envelope := mustEnvelope(t, bytes.Repeat([]byte{0x11}, secret.MasterKeySize))
	plaintext := []byte("same plaintext")
	purpose := secret.PurposeRequestCapture

	nonces := make(map[string]struct{})
	ciphertexts := make(map[string]struct{})

	for i := 0; i < 32; i++ {
		encoded, err := envelope.Encrypt(plaintext, purpose)
		if err != nil {
			t.Fatalf("第 %d 次 Encrypt() error = %v", i, err)
		}
		_, nonce, ciphertext := splitEnvelope(t, encoded)

		nonceKey := base64.RawURLEncoding.EncodeToString(nonce)
		if _, exists := nonces[nonceKey]; exists {
			t.Fatalf("第 %d 次加密复用了 nonce %s", i, nonceKey)
		}
		nonces[nonceKey] = struct{}{}

		ciphertextKey := base64.RawURLEncoding.EncodeToString(ciphertext)
		if _, exists := ciphertexts[ciphertextKey]; exists {
			t.Fatalf("第 %d 次加密得到相同密文", i)
		}
		ciphertexts[ciphertextKey] = struct{}{}

		decrypted, err := envelope.Decrypt(encoded, purpose)
		if err != nil {
			t.Fatalf("第 %d 次 Decrypt() error = %v", i, err)
		}
		if !bytes.Equal(decrypted, plaintext) {
			t.Fatalf("第 %d 次解密结果不匹配", i)
		}
	}
}

func TestDecryptWithWrongMasterKeyFails(t *testing.T) {
	plaintext := "sk-wrong-master-key-test"
	encoder := mustEnvelope(t, bytes.Repeat([]byte{0x01}, secret.MasterKeySize))
	decoder := mustEnvelope(t, bytes.Repeat([]byte{0x02}, secret.MasterKeySize))

	encoded, err := encoder.EncryptString(plaintext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}

	got, err := decoder.DecryptString(encoded, secret.PurposeChannelKey)
	if !errors.Is(err, secret.ErrDecryptFailed) {
		t.Fatalf("DecryptString() error = %v, want ErrDecryptFailed", err)
	}
	if got != "" {
		t.Errorf("失败时返回明文 %q", got)
	}
	assertErrorHides(t, err, plaintext)
}

func TestDecryptWithWrongPurposeFails(t *testing.T) {
	plaintext := "sk-purpose-bound"
	envelope := mustEnvelope(t, bytes.Repeat([]byte{0x03}, secret.MasterKeySize))

	encoded, err := envelope.EncryptString(plaintext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}

	got, err := envelope.DecryptString(encoded, secret.PurposeRequestCapture)
	if !errors.Is(err, secret.ErrDecryptFailed) {
		t.Fatalf("跨用途解密 error = %v, want ErrDecryptFailed", err)
	}
	if got != "" {
		t.Errorf("失败时返回明文 %q", got)
	}
	assertErrorHides(t, err, plaintext)
}

func TestTamperedEnvelopeFails(t *testing.T) {
	plaintext := "sk-tamper-detection-test"
	envelope := mustEnvelope(t, bytes.Repeat([]byte{0x04}, secret.MasterKeySize))

	encoded, err := envelope.EncryptString(plaintext, secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}
	version, nonce, ciphertext := splitEnvelope(t, encoded)

	t.Run("篡改密文首字节", func(t *testing.T) {
		tampered := append([]byte(nil), ciphertext...)
		tampered[0] ^= 0xff
		assertDecryptFails(t, envelope, encodeEnvelope(version, nonce, tampered), plaintext)
	})

	t.Run("篡改认证标签", func(t *testing.T) {
		tampered := append([]byte(nil), ciphertext...)
		tampered[len(tampered)-1] ^= 0x01
		assertDecryptFails(t, envelope, encodeEnvelope(version, nonce, tampered), plaintext)
	})

	t.Run("篡改 nonce", func(t *testing.T) {
		tampered := append([]byte(nil), nonce...)
		tampered[0] ^= 0x80
		assertDecryptFails(t, envelope, encodeEnvelope(version, tampered, ciphertext), plaintext)
	})

	t.Run("截断密文", func(t *testing.T) {
		assertDecryptFails(t, envelope, encodeEnvelope(version, nonce, ciphertext[:len(ciphertext)-1]), plaintext)
	})
}

func assertDecryptFails(t *testing.T, envelope *secret.Envelope, encoded, plaintext string) {
	t.Helper()
	got, err := envelope.DecryptString(encoded, secret.PurposeChannelKey)
	if !errors.Is(err, secret.ErrDecryptFailed) {
		t.Fatalf("DecryptString() error = %v, want ErrDecryptFailed", err)
	}
	if got != "" {
		t.Errorf("失败时返回明文 %q", got)
	}
	assertErrorHides(t, err, plaintext)
}

func encodeEnvelope(version string, nonce, ciphertext []byte) string {
	return version + ":" +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(ciphertext)
}

func TestUnsupportedAndMalformedEnvelope(t *testing.T) {
	envelope := mustEnvelope(t, bytes.Repeat([]byte{0x05}, secret.MasterKeySize))
	valid, err := envelope.EncryptString("sk-version-check", secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}
	version, nonce, ciphertext := splitEnvelope(t, valid)
	if version != "v1" {
		t.Fatalf("版本 = %q, want v1", version)
	}

	validNonce := base64.RawURLEncoding.EncodeToString(nonce)
	validCiphertext := base64.RawURLEncoding.EncodeToString(ciphertext)
	shortNonce := base64.RawURLEncoding.EncodeToString(make([]byte, 8))
	shortTag := base64.RawURLEncoding.EncodeToString(make([]byte, 15))

	tests := []struct {
		name     string
		envelope string
		want     error
	}{
		{name: "空信封", envelope: "", want: secret.ErrMalformedEnvelope},
		{name: "缺少字段", envelope: "v1", want: secret.ErrMalformedEnvelope},
		{name: "仅版本", envelope: "v1:", want: secret.ErrMalformedEnvelope},
		{name: "缺少密文", envelope: "v1:" + validNonce, want: secret.ErrMalformedEnvelope},
		{name: "字段过多", envelope: valid + ":extra", want: secret.ErrMalformedEnvelope},
		{name: "空 nonce", envelope: "v1::" + validCiphertext, want: secret.ErrMalformedEnvelope},
		{name: "空密文", envelope: "v1:" + validNonce + ":", want: secret.ErrMalformedEnvelope},
		{name: "非 base64 字符", envelope: "v1:!!!!:????", want: secret.ErrMalformedEnvelope},
		{name: "nonce 长度错误", envelope: "v1:" + shortNonce + ":" + validCiphertext, want: secret.ErrMalformedEnvelope},
		{name: "密文短于标签", envelope: "v1:" + validNonce + ":" + shortTag, want: secret.ErrMalformedEnvelope},
		{name: "v2 版本", envelope: "v2:" + validNonce + ":" + validCiphertext, want: secret.ErrUnsupportedVersion},
		{name: "大写版本", envelope: "V1:" + validNonce + ":" + validCiphertext, want: secret.ErrUnsupportedVersion},
		{name: "版本后缀", envelope: "v1x:" + validNonce + ":" + validCiphertext, want: secret.ErrUnsupportedVersion},
		{name: "未知前缀", envelope: "legacy:" + validNonce + ":" + validCiphertext, want: secret.ErrUnsupportedVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := envelope.Decrypt(tt.envelope, secret.PurposeChannelKey)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decrypt(%q) error = %v, want %v", tt.envelope, err, tt.want)
			}
		})
	}
}

func TestEncryptDecryptRequirePurpose(t *testing.T) {
	envelope := mustEnvelope(t, bytes.Repeat([]byte{0x06}, secret.MasterKeySize))

	if _, err := envelope.EncryptString("sk-no-purpose", ""); !errors.Is(err, secret.ErrInvalidPurpose) {
		t.Fatalf("EncryptString(purpose=\"\") error = %v, want ErrInvalidPurpose", err)
	}

	valid, err := envelope.EncryptString("sk-no-purpose", secret.PurposeChannelKey)
	if err != nil {
		t.Fatalf("EncryptString() error = %v", err)
	}
	if _, err := envelope.Decrypt(valid, ""); !errors.Is(err, secret.ErrInvalidPurpose) {
		t.Fatalf("Decrypt(purpose=\"\") error = %v, want ErrInvalidPurpose", err)
	}
}

func TestNilEnvelope(t *testing.T) {
	var envelope *secret.Envelope

	if _, err := envelope.Encrypt([]byte("x"), secret.PurposeChannelKey); !errors.Is(err, secret.ErrNilEnvelope) {
		t.Errorf("Encrypt() error = %v, want ErrNilEnvelope", err)
	}
	if _, err := envelope.Decrypt("v1:a:b", secret.PurposeChannelKey); !errors.Is(err, secret.ErrNilEnvelope) {
		t.Errorf("Decrypt() error = %v, want ErrNilEnvelope", err)
	}
}
