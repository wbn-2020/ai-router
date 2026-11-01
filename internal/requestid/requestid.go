// Package requestid 提供跨 HTTP 中间件与数据面共享的请求 ID 上下文键。
//
// 服务端中间件生成 ID 并写入 context；数据面的日志、诊断环和代理使用同一个
// ID。package 只依赖标准库，避免 server 与 gateway 之间产生循环依赖。
package requestid

import "context"

type contextKey struct{}

// With 返回携带请求 ID 的 context。
func With(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, contextKey{}, id)
}

// From 返回 context 中的请求 ID；不存在时返回空串。
func From(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(contextKey{}).(string)
	return id
}
