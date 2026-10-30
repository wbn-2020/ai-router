// Package adminauth 实现单管理员的密码哈希、opaque session 与登录限流。
//
// 安全约束（见计划 T04）：
//   - 密码只以 PBKDF2-HMAC-SHA256 哈希存储，编码为
//     pbkdf2-sha256$iterations$salt$hash（salt/key 使用无填充标准 base64）；
//   - 密码校验使用常量时间比较；
//   - session token 只在内存与客户端保存，数据库只存 SHA-256；
//   - 同一来源 15 分钟内最多 5 次失败，成功登录后清零。
//
// 本包不依赖 gateway 数据面，只接受 database/sql 兼容的连接。
package adminauth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// PasswordHashAlgorithm 是密码哈希编码中的算法标识。
	PasswordHashAlgorithm = "pbkdf2-sha256"
	// PasswordHashIterations 是生产环境的 PBKDF2 迭代次数。
	PasswordHashIterations = 600000
	// PasswordSaltSize 是随机盐的字节长度。
	PasswordSaltSize = 16
	// PasswordKeySize 是派生密钥的字节长度。
	PasswordKeySize = 32
	// MinPasswordLength 是管理员密码的最短字符数（按 rune 计数）。
	MinPasswordLength = 12
)

const (
	// 解析旧/异常哈希时的合理边界，避免被异常迭代次数拖垮。
	maxPasswordHashIterations = 10_000_000
	minPasswordSaltSize       = 8
	maxPasswordSaltSize       = 64
	minPasswordKeySize        = 16
	maxPasswordKeySize        = 64
)

var (
	// ErrPasswordTooShort 表示密码长度不足 MinPasswordLength。
	ErrPasswordTooShort = errors.New("adminauth: 密码长度不足")
	// ErrInvalidPasswordHash 表示存储的密码哈希无法解析。
	ErrInvalidPasswordHash = errors.New("adminauth: 密码哈希格式非法")
)

// dummySalt 用于“尚无管理员记录”路径的等时校验；固定值即可，不参与真实存储。
var dummySalt = []byte("adminauth-dummy-")

// ValidatePassword 检查密码是否满足最短长度要求。
//
// 按 Unicode 字符计数，多字节密码不会被误判为过短。
func ValidatePassword(password string) error {
	if utf8.RuneCountInString(password) < MinPasswordLength {
		return fmt.Errorf("%w（至少 %d 个字符）", ErrPasswordTooShort, MinPasswordLength)
	}
	return nil
}

// HashPassword 用生产参数生成可入库的密码哈希。
//
// 每次调用都会生成新的 16 字节随机盐；返回值不包含明文密码。
func HashPassword(password string) (string, error) {
	if err := ValidatePassword(password); err != nil {
		return "", err
	}

	salt := make([]byte, PasswordSaltSize)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", fmt.Errorf("adminauth: 生成密码盐失败：%w", err)
	}
	return hashPassword(password, salt, PasswordHashIterations, PasswordKeySize)
}

// VerifyPassword 校验明文密码与存储哈希是否匹配。
//
// 迭代次数、盐与密钥长度全部来自哈希编码，便于未来调整参数；
// 比较使用 crypto/subtle，耗时与不匹配的字节位置无关。
// 哈希格式非法时返回 ErrInvalidPasswordHash。
func VerifyPassword(encoded, password string) (bool, error) {
	iterations, salt, want, err := parsePasswordHash(encoded)
	if err != nil {
		return false, err
	}

	got, err := pbkdf2.Key(sha256.New, password, salt, iterations, len(want))
	if err != nil {
		// 参数来自已校验的编码，正常不会失败；错误不包含密码。
		return false, ErrInvalidPasswordHash
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// hashPassword 按显式参数生成哈希；生产代码只通过 HashPassword 调用。
func hashPassword(password string, salt []byte, iterations, keySize int) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, salt, iterations, keySize)
	if err != nil {
		return "", fmt.Errorf("adminauth: 派生密码哈希失败：%w", err)
	}
	return strings.Join([]string{
		PasswordHashAlgorithm,
		strconv.Itoa(iterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	}, "$"), nil
}

// verifyDummy 在没有管理员记录时执行与真实校验相近的工作量，
// 避免通过响应耗时判断管理员是否已初始化。
func verifyDummy(password string) {
	_, _ = pbkdf2.Key(sha256.New, password, dummySalt, PasswordHashIterations, PasswordKeySize)
}

// parsePasswordHash 解析 pbkdf2-sha256$iterations$salt$hash。
func parsePasswordHash(encoded string) (int, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != PasswordHashAlgorithm {
		return 0, nil, nil, ErrInvalidPasswordHash
	}

	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > maxPasswordHashIterations {
		return 0, nil, nil, ErrInvalidPasswordHash
	}

	salt, ok := decodeHashPart(parts[2])
	if !ok || len(salt) < minPasswordSaltSize || len(salt) > maxPasswordSaltSize {
		return 0, nil, nil, ErrInvalidPasswordHash
	}

	key, ok := decodeHashPart(parts[3])
	if !ok || len(key) < minPasswordKeySize || len(key) > maxPasswordKeySize {
		return 0, nil, nil, ErrInvalidPasswordHash
	}

	return iterations, salt, key, nil
}

// decodeHashPart 同时接受无填充与带填充的标准 base64。
func decodeHashPart(raw string) ([]byte, bool) {
	if raw == "" {
		return nil, false
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(raw); err == nil {
		return decoded, true
	}
	if decoded, err := base64.StdEncoding.DecodeString(raw); err == nil {
		return decoded, true
	}
	return nil, false
}
