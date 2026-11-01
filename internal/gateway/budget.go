package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"ai-router/internal/routing"
)

var errFirstContentBudget = errors.New("gateway: first content budget exceeded")

func timeoutOrDefault(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

func requestFailure(ctx context.Context, err error) proxyFailure {
	if errors.Is(context.Cause(ctx), errFirstContentBudget) {
		return proxyFailure{Status: http.StatusGatewayTimeout, Code: "gateway_first_content_timeout", Type: errorTypeServer,
			Message: "The first response content deadline was exceeded.", Scope: routing.FailureScopeRequest}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return proxyFailure{Status: http.StatusGatewayTimeout, Code: "gateway_request_timeout", Type: errorTypeServer,
			Message: "The gateway request deadline was exceeded.", Scope: routing.FailureScopeRequest}
	}
	if ctx.Err() != nil {
		return classifyUpstreamError(ctx.Err())
	}
	return classifyUpstreamError(err)
}

func downstreamFailure(ctx context.Context, err error) proxyFailure {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(context.Cause(ctx), errFirstContentBudget) {
		return requestFailure(ctx, err)
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return proxyFailure{Status: http.StatusGatewayTimeout, Code: "downstream_write_timeout", Type: errorTypeServer,
			Message: "The response write deadline was exceeded.", Scope: routing.FailureScopeRequest}
	}
	if ctx.Err() != nil {
		return requestFailure(ctx, err)
	}
	return proxyFailure{Status: http.StatusBadGateway, Code: "downstream_write_failed", Type: errorTypeServer,
		Message: "The response could not be delivered.", Scope: routing.FailureScopeRequest}
}

func renewWriteDeadline(ctx context.Context, w http.ResponseWriter, timeout time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	if total, ok := ctx.Deadline(); ok && total.Before(deadline) {
		deadline = total
	}
	return http.NewResponseController(w).SetWriteDeadline(deadline)
}

// 首语义提交原子撤除首内容计时器，父级总预算仍约束整条流。
type firstContentBudget struct {
	mu        sync.Mutex
	timer     *time.Timer
	expired   bool
	committed bool
	cancel    context.CancelCauseFunc
}

func startFirstContentBudget(ctx context.Context, timeout time.Duration) (context.Context, *firstContentBudget) {
	ctx, cancel := context.WithCancelCause(ctx)
	b := &firstContentBudget{cancel: cancel}
	b.timer = time.AfterFunc(timeout, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if !b.committed {
			b.expired = true
			cancel(errFirstContentBudget)
		}
	})
	return ctx, b
}

func (b *firstContentBudget) commit() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.expired {
		return errFirstContentBudget
	}
	b.committed = true
	b.timer.Stop()
	return nil
}

func (b *firstContentBudget) close() {
	b.mu.Lock()
	b.committed = true
	b.timer.Stop()
	b.cancel(context.Canceled)
	b.mu.Unlock()
}
