package secret_test

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"ai-router/internal/secret"
)

func assertNotContains(t *testing.T, got string, forbidden ...string) {
	t.Helper()
	for _, value := range forbidden {
		if value != "" && strings.Contains(got, value) {
			t.Errorf("输出仍包含敏感值 %q: %q", value, got)
		}
	}
}

func TestMaskKey(t *testing.T) {
	tests := []struct {
		name string
		key  string
		want string
	}{
		{
			name: "验收示例 sk-***abcd",
			key:  "sk-123abcd",
			want: "sk-***abcd",
		},
		{
			name: "长 Key 保留前 3 后 4",
			key:  "sk-1234567890abcd",
			want: "sk-" + strings.Repeat("*", 10) + "abcd",
		},
		{
			name: "短 Key 全部星号",
			key:  "short",
			want: "*****",
		},
		{
			name: "9 字符 Key 全部星号",
			key:  "123456789",
			want: "*********",
		},
		{
			name: "空 Key",
			key:  "",
			want: "",
		},
		{
			name: "已经脱敏的 Key 保持形状",
			key:  "sk-***abcd",
			want: "sk-***abcd",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secret.MaskKey(tt.key)
			if got != tt.want {
				t.Errorf("MaskKey(%q) = %q, want %q", tt.key, got, tt.want)
			}
			if tt.key != "" && got == tt.key && !strings.Contains(tt.key, "*") {
				t.Errorf("MaskKey(%q) 未改变输入", tt.key)
			}
			if tt.key != "" && !strings.Contains(tt.key, "*") {
				assertNotContains(t, got, tt.key)
			}
		})
	}
}

func TestMaskKeyHandlesUnicodeByRune(t *testing.T) {
	key := "密钥-abcdefgh"
	got := secret.MaskKey(key)

	if utf8.RuneCountInString(got) != utf8.RuneCountInString(key) {
		t.Errorf("MaskKey 结果 rune 数 = %d, want %d", utf8.RuneCountInString(got), utf8.RuneCountInString(key))
	}
	if !strings.HasPrefix(got, "密钥-") {
		t.Errorf("MaskKey 前缀 = %q, want 密钥-", got)
	}
	if !strings.HasSuffix(got, "efgh") {
		t.Errorf("MaskKey 后缀 = %q, want efgh", got)
	}
}

func TestRedactValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
		forbid   []string
	}{
		{
			name:     "authorization header",
			input:    "authorization: Bearer sk-abcdefghijklmnop",
			contains: secret.RedactedMarker,
			forbid:   []string{"sk-abcdefghijklmnop", "Bearer sk-abcdefghijklmnop"},
		},
		{
			name:     "大小写混合 x-api-key",
			input:    "X-Api-Key: sk-1234567890",
			contains: secret.RedactedMarker,
			forbid:   []string{"sk-1234567890"},
		},
		{
			name:     "cookie 多值",
			input:    "cookie: session=abc; token=def",
			contains: secret.RedactedMarker,
			forbid:   []string{"session=abc", "token=def"},
		},
		{
			name:     "set-cookie",
			input:    "set-cookie: sid=very-secret; HttpOnly",
			contains: secret.RedactedMarker,
			forbid:   []string{"sid=very-secret"},
		},
		{
			name:     "proxy-authorization Basic",
			input:    "proxy-authorization: Basic dXNlcjpwYXNzd29yZA==",
			contains: secret.RedactedMarker,
			forbid:   []string{"dXNlcjpwYXNzd29yZA=="},
		},
		{
			name:     "裸 Bearer",
			input:    "request failed: Bearer abcdefghijklmnop",
			contains: "Bearer " + secret.RedactedMarker,
			forbid:   []string{"abcdefghijklmnop"},
		},
		{
			name:     "裸上游 Key",
			input:    "upstream key sk-abcdefghijklmnop leaked",
			contains: secret.RedactedMarker,
			forbid:   []string{"sk-abcdefghijklmnop"},
		},
		{
			name:     "客户端 Key",
			input:    "client gw_abcdefghijklmnop used",
			contains: secret.RedactedMarker,
			forbid:   []string{"gw_abcdefghijklmnop"},
		},
		{
			name:     "api_key 查询参数",
			input:    "GET /v1/models?api_key=sk-abcdefghijklmnop",
			contains: secret.RedactedMarker,
			forbid:   []string{"sk-abcdefghijklmnop"},
		},
		{
			name:     "普通日志保持不变",
			input:    "status=200 latency=42ms model=gpt-5",
			contains: "",
			forbid:   nil,
		},
		{
			name:     "包含 sk 的普通单词不变",
			input:    "task-force disk-usage risk-free",
			contains: "",
			forbid:   nil,
		},
		{
			name:     "已脱敏 Key 不被再次破坏",
			input:    "masked key sk-***abcd",
			contains: "sk-***abcd",
			forbid:   []string{secret.RedactedMarker},
		},
		{
			name:     "空字符串",
			input:    "",
			contains: "",
			forbid:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secret.RedactValue(tt.input)
			if tt.contains != "" && !strings.Contains(got, tt.contains) {
				t.Errorf("RedactValue(%q) = %q, want contains %q", tt.input, got, tt.contains)
			}
			assertNotContains(t, got, tt.forbid...)
			if tt.contains == "" && tt.forbid == nil && got != tt.input {
				t.Errorf("RedactValue(%q) = %q, want unchanged", tt.input, got)
			}
		})
	}
}

func TestRedactMap(t *testing.T) {
	input := map[string]string{
		"Authorization":       "Bearer sk-abcdefghijklmnop",
		"x-api-key":           "sk-1234567890",
		"API_KEY":             "sk-api-key-value-1234",
		"Cookie":              "session=abc; token=def",
		"Set-Cookie":          "sid=secret; HttpOnly",
		"Proxy-Authorization": "Basic dXNlcjpwYXNz",
		"Content-Type":        "application/json",
		"X-Request-ID":        "req-123",
		"note":                "upstream sk-abcdefghijklmnop",
	}

	got := secret.RedactMap(input)
	if got == nil {
		t.Fatal("RedactMap() 返回 nil")
	}

	for _, name := range []string{
		"Authorization",
		"x-api-key",
		"API_KEY",
		"Cookie",
		"Set-Cookie",
		"Proxy-Authorization",
	} {
		if got[name] != secret.RedactedMarker {
			t.Errorf("RedactMap[%q] = %q, want %q", name, got[name], secret.RedactedMarker)
		}
	}
	if got["Content-Type"] != "application/json" {
		t.Errorf("Content-Type = %q, want 原值", got["Content-Type"])
	}
	if got["X-Request-ID"] != "req-123" {
		t.Errorf("X-Request-ID = %q, want 原值", got["X-Request-ID"])
	}
	assertNotContains(t, got["note"], "sk-abcdefghijklmnop")

	// 入参不得被修改。
	if input["Authorization"] != "Bearer sk-abcdefghijklmnop" {
		t.Errorf("RedactMap 修改了入参")
	}
	if original := input["Cookie"]; original != "session=abc; token=def" {
		t.Errorf("RedactMap 修改了 Cookie 入参: %q", original)
	}
}

func TestRedactMapNil(t *testing.T) {
	if got := secret.RedactMap(nil); got != nil {
		t.Errorf("RedactMap(nil) = %#v, want nil", got)
	}
}

func TestRedactHeader(t *testing.T) {
	input := http.Header{
		"Authorization":       {"Bearer sk-abcdefghijklmnop", "Bearer sk-second-value"},
		"X-Api-Key":           {"sk-1234567890"},
		"Cookie":              {"session=abc; token=def"},
		"Set-Cookie":          {"sid=secret; HttpOnly"},
		"Proxy-Authorization": {"Basic dXNlcjpwYXNz"},
		"Content-Type":        {"application/json"},
		"X-Request-Id":        {"req-123"},
		"X-Custom":            {"Bearer sk-embedded-token"},
	}

	got := secret.RedactHeader(input)
	if got == nil {
		t.Fatal("RedactHeader() 返回 nil")
	}

	for _, name := range []string{
		"Authorization",
		"X-Api-Key",
		"Cookie",
		"Set-Cookie",
		"Proxy-Authorization",
	} {
		values := got[name]
		if len(values) == 0 {
			t.Errorf("RedactHeader[%q] 缺失", name)
			continue
		}
		for i, value := range values {
			if value != secret.RedactedMarker {
				t.Errorf("RedactHeader[%q][%d] = %q, want %q", name, i, value, secret.RedactedMarker)
			}
		}
	}
	if got.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want 原值", got.Get("Content-Type"))
	}
	if got.Get("X-Request-Id") != "req-123" {
		t.Errorf("X-Request-Id = %q, want 原值", got.Get("X-Request-Id"))
	}
	assertNotContains(t, got.Get("X-Custom"), "sk-embedded-token")

	// 入参不得被修改，输出与入参不能共享切片。
	if input.Get("Authorization") != "Bearer sk-abcdefghijklmnop" {
		t.Errorf("RedactHeader 修改了入参 Authorization")
	}
	got["Content-Type"][0] = "text/plain"
	if input.Get("Content-Type") != "application/json" {
		t.Errorf("RedactHeader 输出与入参共享切片")
	}
}

func TestRedactHeaderNil(t *testing.T) {
	if got := secret.RedactHeader(nil); got != nil {
		t.Errorf("RedactHeader(nil) = %#v, want nil", got)
	}
}

func TestRedactGeneric(t *testing.T) {
	t.Run("string", func(t *testing.T) {
		got := secret.Redact("authorization: Bearer sk-abcdefghijklmnop")
		if !strings.Contains(got, secret.RedactedMarker) {
			t.Errorf("Redact(string) = %q, want marker", got)
		}
		assertNotContains(t, got, "sk-abcdefghijklmnop")
	})

	t.Run("map", func(t *testing.T) {
		got := secret.Redact(map[string]string{"Authorization": "Bearer sk-abcdefghijklmnop"})
		if got["Authorization"] != secret.RedactedMarker {
			t.Errorf("Redact(map) = %#v, want marker", got)
		}
	})

	t.Run("header", func(t *testing.T) {
		got := secret.Redact(http.Header{"Authorization": {"Bearer sk-abcdefghijklmnop"}})
		if got.Get("Authorization") != secret.RedactedMarker {
			t.Errorf("Redact(http.Header) = %#v, want marker", got.Get("Authorization"))
		}
	})

	t.Run("map of slices", func(t *testing.T) {
		got := secret.Redact(map[string][]string{"Authorization": {"Bearer sk-abcdefghijklmnop"}})
		if !reflect.DeepEqual(got["Authorization"], []string{secret.RedactedMarker}) {
			t.Errorf("Redact(map[string][]string) = %#v", got["Authorization"])
		}
	})

	t.Run("other type unchanged", func(t *testing.T) {
		if got := secret.Redact(42); got != 42 {
			t.Errorf("Redact(42) = %d, want 42", got)
		}
	})
}
