package adminauth

import (
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func TestHashPasswordUsesProductionParameters(t *testing.T) {
	const password = "correct-horse-battery"

	encoded, err := HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if strings.Contains(encoded, password) {
		t.Fatalf("哈希编码包含明文密码：%s", encoded)
	}

	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		t.Fatalf("哈希编码应有 4 段，得到 %d 段：%s", len(parts), encoded)
	}
	if parts[0] != PasswordHashAlgorithm {
		t.Fatalf("算法标识 = %q，期望 %q", parts[0], PasswordHashAlgorithm)
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("迭代次数不是整数：%q", parts[1])
	}
	if iterations != PasswordHashIterations {
		t.Fatalf("迭代次数 = %d，期望 %d", iterations, PasswordHashIterations)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("盐不是合法 base64：%v", err)
	}
	if len(salt) != PasswordSaltSize {
		t.Fatalf("盐长度 = %d，期望 %d", len(salt), PasswordSaltSize)
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		t.Fatalf("哈希不是合法 base64：%v", err)
	}
	if len(key) != PasswordKeySize {
		t.Fatalf("哈希长度 = %d，期望 %d", len(key), PasswordKeySize)
	}

	if ok, err := VerifyPassword(encoded, password); err != nil || !ok {
		t.Fatalf("正确密码校验失败：ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(encoded, password+"x"); err != nil || ok {
		t.Fatalf("错误密码校验结果异常：ok=%v err=%v", ok, err)
	}

	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("第二次 HashPassword: %v", err)
	}
	if second == encoded {
		t.Fatal("同一密码两次哈希得到相同编码，盐未随机化")
	}
	secondParts := strings.Split(second, "$")
	if len(secondParts) != 4 || secondParts[2] == parts[2] {
		t.Fatal("两次哈希的盐相同")
	}
}

func TestHashPasswordEnforcesMinimumLength(t *testing.T) {
	for _, password := range []string{
		"",
		"short",
		strings.Repeat("a", MinPasswordLength-1),
	} {
		if _, err := HashPassword(password); !errors.Is(err, ErrPasswordTooShort) {
			t.Fatalf("密码 %q 应返回 ErrPasswordTooShort，得到 %v", password, err)
		}
	}

	// 恰好 12 个字符的多字节密码必须被接受（按 rune 计数）。
	const multibyte = "密码密码密码密码密码密码"
	if len([]rune(multibyte)) != MinPasswordLength {
		t.Fatalf("测试密码 rune 数 = %d，期望 %d", len([]rune(multibyte)), MinPasswordLength)
	}
	if _, err := HashPassword(multibyte); err != nil {
		t.Fatalf("12 字符多字节密码应被接受：%v", err)
	}
}

func TestVerifyPasswordRejectsMalformedHash(t *testing.T) {
	salt := base64.RawStdEncoding.EncodeToString([]byte("0123456789abcdef"))
	key := base64.RawStdEncoding.EncodeToString(make([]byte, PasswordKeySize))
	shortSalt := base64.RawStdEncoding.EncodeToString([]byte("short"))
	shortKey := base64.RawStdEncoding.EncodeToString(make([]byte, 8))

	cases := map[string]string{
		"空字符串":       "",
		"段数不足":       "pbkdf2-sha256$600000$" + salt,
		"算法不匹配":      "bcrypt$600000$" + salt + "$" + key,
		"迭代次数非数字":    "pbkdf2-sha256$many$" + salt + "$" + key,
		"迭代次数为零":     "pbkdf2-sha256$0$" + salt + "$" + key,
		"迭代次数超上限":    "pbkdf2-sha256$10000001$" + salt + "$" + key,
		"盐非 base64":  "pbkdf2-sha256$600000$!!!$" + key,
		"盐过短":        "pbkdf2-sha256$600000$" + shortSalt + "$" + key,
		"哈希非 base64": "pbkdf2-sha256$600000$" + salt + "$!!!",
		"哈希过短":       "pbkdf2-sha256$600000$" + salt + "$" + shortKey,
	}

	for name, encoded := range cases {
		t.Run(name, func(t *testing.T) {
			ok, err := VerifyPassword(encoded, "whatever-password")
			if ok {
				t.Fatal("非法哈希不应校验通过")
			}
			if !errors.Is(err, ErrInvalidPasswordHash) {
				t.Fatalf("错误 = %v，期望 ErrInvalidPasswordHash", err)
			}
		})
	}
}

func TestVerifyPasswordHonorsEncodedIterations(t *testing.T) {
	encoded, err := hashPassword("short-but-verifiable", []byte("0123456789abcdef"), 2, PasswordKeySize)
	if err != nil {
		t.Fatalf("hashPassword: %v", err)
	}
	if ok, err := VerifyPassword(encoded, "short-but-verifiable"); err != nil || !ok {
		t.Fatalf("低迭代哈希校验失败：ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(encoded, "different-password"); err != nil || ok {
		t.Fatalf("错误密码校验结果异常：ok=%v err=%v", ok, err)
	}
}
