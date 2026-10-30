package secret

import (
	"net/http"
	"regexp"
	"strings"
)

const (
	// RedactedMarker 是日志与调试输出中替换敏感值的固定占位符。
	RedactedMarker = "<redacted>"

	maskPrefixLen = 3
	maskSuffixLen = 4
	// maskMinHidden 要求至少隐藏 3 个字符，避免短 Key 暴露过多内容。
	maskMinHidden = 3
)

var (
	// 形如 "authorization: Bearer xxx"、"cookie=..." 的键值片段整体替换。
	sensitiveAssignmentPattern = regexp.MustCompile(
		`(?i)\b(authorization|proxy-authorization|x-api-key|api[_-]?key|access[_-]?token|cookie|set-cookie)\b(\s*[:=]\s*)[^\r\n]+`,
	)
	// 裸 Bearer/Basic 凭据。
	bearerTokenPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=:-]{8,}`)
	// 常见上游/客户端 Key 前缀：sk-、gw_ 等。
	secretKeyPattern = regexp.MustCompile(`\b(?:sk|gw|pk|rk|xai|ghp|gho|ghu|ghs|ghr)[-_][A-Za-z0-9_-]{4,}`)
)

// MaskKey 返回可安全展示的上游 Key 脱敏形式。
//
// 规则（T03 合同）：
//   - 足够长的 Key 保留前 3 个字符与后 4 个字符，中间每个字符替换为 *；
//   - 不足 10 个字符的 Key 全部替换为 *，避免短 Key 暴露过多字符。
//
// 示例：sk-1234abcd -> sk-***abcd。
func MaskKey(key string) string {
	runes := []rune(key)
	if len(runes) < maskPrefixLen+maskSuffixLen+maskMinHidden {
		return strings.Repeat("*", len(runes))
	}
	return string(runes[:maskPrefixLen]) +
		strings.Repeat("*", len(runes)-maskPrefixLen-maskSuffixLen) +
		string(runes[len(runes)-maskSuffixLen:])
}

// RedactValue 对日志文本做尽力而为的敏感信息替换。
//
// 它覆盖：
//   - authorization、proxy-authorization、x-api-key、api_key、access_token、
//     cookie、set-cookie 等键值片段；
//   - Bearer/Basic 凭据；
//   - 形如 sk-...、gw_... 的 Key。
//
// 非敏感文本原样返回。
func RedactValue(value string) string {
	if value == "" {
		return ""
	}

	out := sensitiveAssignmentPattern.ReplaceAllString(value, "${1}${2}"+RedactedMarker)
	out = bearerTokenPattern.ReplaceAllString(out, "${1} "+RedactedMarker)
	out = secretKeyPattern.ReplaceAllString(out, RedactedMarker)
	return out
}

// RedactMap 返回脱敏后的新 map，不修改入参。
//
// 敏感名称的值固定替换为 RedactedMarker；其余值仍会经过 RedactValue，
// 防止自定义字段中夹带上游 Key。
func RedactMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	redacted := make(map[string]string, len(values))
	for name, value := range values {
		if isSensitiveName(name) {
			redacted[name] = RedactedMarker
			continue
		}
		redacted[name] = RedactValue(value)
	}
	return redacted
}

// RedactHeader 返回脱敏后的新 http.Header，不修改入参。
func RedactHeader(header http.Header) http.Header {
	if header == nil {
		return nil
	}

	redacted := make(http.Header, len(header))
	for name, values := range header {
		if values == nil {
			redacted[name] = nil
			continue
		}

		copied := make([]string, len(values))
		sensitive := isSensitiveName(name)
		for i, value := range values {
			if sensitive {
				copied[i] = RedactedMarker
				continue
			}
			copied[i] = RedactValue(value)
		}
		redacted[name] = copied
	}
	return redacted
}

// Redact 按运行时类型脱敏 string、map[string]string、http.Header 或
// map[string][]string；其他类型原样返回。
//
// 泛型签名让调用方无需关心具体类型，同时保留静态返回类型：
//
//	secret.Redact(logLine)      // string
//	secret.Redact(header)       // http.Header
//	secret.Redact(metadataMap)  // map[string]string
func Redact[T any](value T) T {
	switch v := any(value).(type) {
	case string:
		return any(RedactValue(v)).(T)
	case map[string]string:
		return any(RedactMap(v)).(T)
	case http.Header:
		return any(RedactHeader(v)).(T)
	case map[string][]string:
		return any(map[string][]string(RedactHeader(http.Header(v)))).(T)
	default:
		return value
	}
}

// isSensitiveName 对 header / 日志字段名做大小写不敏感匹配。
func isSensitiveName(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "authorization",
		"proxy-authorization",
		"x-api-key",
		"api-key",
		"api_key",
		"access-token",
		"access_token",
		"cookie",
		"set-cookie":
		return true
	default:
		return false
	}
}
