package gateway

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"
)

const (
	authorizationHeader = "Authorization"
	apiKeyHeader        = "X-Api-Key"
	bearerScheme        = "Bearer"
)

// clientKeyAuth 校验唯一的客户端 API Key。
//
// 构造时只保留 Key 的 SHA-256 摘要，Handler 生命周期内不保存明文；比较使用
// subtle.ConstantTimeCompare，避免按字节短路泄露 Key 前缀。
type clientKeyAuth struct {
	expected [sha256.Size]byte
}

func newClientKeyAuth(key string) clientKeyAuth {
	return clientKeyAuth{expected: sha256.Sum256([]byte(key))}
}

// authorize 接受 Authorization: Bearer 或 x-api-key。
//
// Authorization 存在但不是合法 Bearer 时回退到 x-api-key，使两种单 Key
// 传递方式保持等价；两者都无效时由调用方返回统一的 401。
func (a clientKeyAuth) authorize(r *http.Request) bool {
	presented := clientKeyFromRequest(r)
	if presented == "" {
		return false
	}
	actual := sha256.Sum256([]byte(presented))
	return subtle.ConstantTimeCompare(a.expected[:], actual[:]) == 1
}

func clientKeyFromRequest(r *http.Request) string {
	if r == nil {
		return ""
	}

	if raw := strings.TrimSpace(r.Header.Get(authorizationHeader)); raw != "" {
		if scheme, token, ok := strings.Cut(raw, " "); ok && strings.EqualFold(scheme, bearerScheme) {
			if token = strings.TrimSpace(token); token != "" {
				return token
			}
		}
	}
	return strings.TrimSpace(r.Header.Get(apiKeyHeader))
}
