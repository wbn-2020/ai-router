// Package secret 提供上游凭据的 AES-256-GCM 加密信封与日志脱敏。
//
// 信封格式固定为 v1:<base64url(nonce)>:<base64url(ciphertext)>：
//   - nonce 为 12 字节随机数，每次加密都重新生成；
//   - ciphertext 为 GCM 密文，末尾附带 16 字节认证标签；
//   - AAD 同时绑定信封版本与调用方声明的用途（purpose），防止密文被跨用途复用。
//
// 主密钥只存在于进程内存中；所有错误信息都不回显主密钥、明文或密文。
package secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// MasterKeySize 是 AES-256 主密钥的字节长度。
	MasterKeySize = 32
	// EnvelopeVersion 是当前信封版本。
	EnvelopeVersion = "v1"

	// nonceSize 是 GCM 推荐的 12 字节随机 nonce。
	nonceSize = 12
	// tagSize 是 GCM 认证标签的字节长度。
	tagSize = 16
)

// 用途常量用于把同一主密钥下的不同数据域隔离开。
const (
	// PurposeChannelKey 加密渠道上游 Key。
	PurposeChannelKey = "channel-upstream-key"
	// PurposeRequestCapture 加密管理员按需保存的请求/响应 body。
	PurposeRequestCapture = "request-capture"
)

var (
	// ErrInvalidMasterKey 表示主密钥缺失、不是 base64 或长度不是 32 字节。
	ErrInvalidMasterKey = errors.New("secret: 主密钥无效，必须为 base64 编码的 32 字节")
	// ErrInvalidPurpose 表示调用方没有声明加密用途。
	ErrInvalidPurpose = errors.New("secret: 加密用途不能为空")
	// ErrMalformedEnvelope 表示信封结构、nonce 长度或密文长度非法。
	ErrMalformedEnvelope = errors.New("secret: 信封格式非法")
	// ErrUnsupportedVersion 表示信封版本不是当前支持的 v1。
	ErrUnsupportedVersion = errors.New("secret: 不支持的信封版本")
	// ErrDecryptFailed 表示认证解密失败（错误主密钥、篡改密文或用途不匹配）。
	ErrDecryptFailed = errors.New("secret: 解密失败")
	// ErrNilEnvelope 表示在未初始化的信封上调用了方法。
	ErrNilEnvelope = errors.New("secret: 信封未初始化")
)

// Envelope 使用 AES-256-GCM 加解密上游凭据。
//
// Envelope 可被并发使用：cipher.AEAD 本身是并发安全的，且每次加密都使用
// 独立的随机 nonce。
type Envelope struct {
	aead cipher.AEAD
}

// ParseMasterKey 解析 GATEWAY_MASTER_KEY：base64（标准编码，允许无填充）
// 解码后必须恰好为 32 字节。
//
// 错误信息不包含输入值。
func ParseMasterKey(encoded string) ([]byte, error) {
	if encoded == "" {
		return nil, ErrInvalidMasterKey
	}

	// 同时接受带 padding 与无 padding 的标准 base64，与 internal/config 的
	// GATEWAY_MASTER_KEY 解析规则保持一致。
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
		decoded, err := encoding.DecodeString(encoded)
		if err != nil {
			continue
		}
		if len(decoded) != MasterKeySize {
			return nil, ErrInvalidMasterKey
		}
		return decoded, nil
	}
	return nil, ErrInvalidMasterKey
}

// NewEnvelope 用 32 字节主密钥构造 AES-256-GCM 信封。
func NewEnvelope(masterKey []byte) (*Envelope, error) {
	if len(masterKey) != MasterKeySize {
		return nil, ErrInvalidMasterKey
	}

	block, err := aes.NewCipher(masterKey)
	if err != nil {
		// 长度已校验，正常不会走到这里；不包装底层错误以免引入额外信息。
		return nil, ErrInvalidMasterKey
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("secret: 初始化 AES-GCM 失败：%w", err)
	}
	return &Envelope{aead: aead}, nil
}

// Encrypt 加密 plaintext 并返回 v1 信封。
//
// purpose 会作为 AAD 的一部分参与认证；解密时必须使用完全相同的用途。
func (e *Envelope) Encrypt(plaintext []byte, purpose string) (string, error) {
	if err := e.validate(purpose); err != nil {
		return "", err
	}

	nonce := make([]byte, nonceSize)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("secret: 生成随机 nonce 失败：%w", err)
	}
	sealed := e.aead.Seal(nil, nonce, plaintext, aad(purpose))

	return EnvelopeVersion + ":" +
		base64.RawURLEncoding.EncodeToString(nonce) + ":" +
		base64.RawURLEncoding.EncodeToString(sealed), nil
}

// EncryptString 是 Encrypt 的字符串便捷入口。
func (e *Envelope) EncryptString(plaintext, purpose string) (string, error) {
	envelope, err := e.Encrypt([]byte(plaintext), purpose)
	if err != nil {
		return "", err
	}
	return envelope, nil
}

// Decrypt 解密 v1 信封并校验版本、用途与 GCM 认证标签。
//
// 任何失败都只返回分类错误，不返回明文，也不回显密文内容。
func (e *Envelope) Decrypt(envelope, purpose string) ([]byte, error) {
	if err := e.validate(purpose); err != nil {
		return nil, err
	}

	nonce, ciphertext, err := parseEnvelope(envelope)
	if err != nil {
		return nil, err
	}
	plaintext, err := e.aead.Open(nil, nonce, ciphertext, aad(purpose))
	if err != nil {
		// GCM 错误本身不含明文；这里仍只返回稳定分类错误。
		return nil, ErrDecryptFailed
	}
	return plaintext, nil
}

// DecryptString 是 Decrypt 的字符串便捷入口。
func (e *Envelope) DecryptString(envelope, purpose string) (string, error) {
	plaintext, err := e.Decrypt(envelope, purpose)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

func (e *Envelope) validate(purpose string) error {
	if e == nil || e.aead == nil {
		return ErrNilEnvelope
	}
	if purpose == "" {
		return ErrInvalidPurpose
	}
	return nil
}

// aad 绑定信封版本与用途。分隔符避免不同版本/用途拼接后产生歧义。
func aad(purpose string) []byte {
	return []byte("ai-router/secret|" + EnvelopeVersion + "|" + purpose)
}

// parseEnvelope 校验信封结构并返回 nonce 与密文。
func parseEnvelope(envelope string) ([]byte, []byte, error) {
	if envelope == "" {
		return nil, nil, ErrMalformedEnvelope
	}

	// 先单独检查版本段：v2:... 等应得到明确的“不支持版本”，而不是格式错误。
	if idx := strings.IndexByte(envelope, ':'); idx >= 0 {
		version := envelope[:idx]
		if version == "" {
			return nil, nil, ErrMalformedEnvelope
		}
		if version != EnvelopeVersion {
			return nil, nil, ErrUnsupportedVersion
		}
	}

	parts := strings.Split(envelope, ":")
	if len(parts) != 3 {
		return nil, nil, ErrMalformedEnvelope
	}

	nonce, err := decodeBase64URL(parts[1])
	if err != nil {
		return nil, nil, ErrMalformedEnvelope
	}
	if len(nonce) != nonceSize {
		return nil, nil, ErrMalformedEnvelope
	}

	ciphertext, err := decodeBase64URL(parts[2])
	if err != nil {
		return nil, nil, ErrMalformedEnvelope
	}
	if len(ciphertext) < tagSize {
		return nil, nil, ErrMalformedEnvelope
	}
	return nonce, ciphertext, nil
}

// decodeBase64URL 解码 base64url；仅为了兼容外部工具产生的带 padding 输出。
func decodeBase64URL(raw string) ([]byte, error) {
	if raw == "" {
		return nil, ErrMalformedEnvelope
	}
	if decoded, err := base64.RawURLEncoding.DecodeString(raw); err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(raw)
}
