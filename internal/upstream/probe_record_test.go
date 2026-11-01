package upstream

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-router/internal/store"
)

type checkingHealthRecorder struct {
	t      *testing.T
	called bool
}

func (r *checkingHealthRecorder) UpdateHealth(ctx context.Context, _ int64, health store.ChannelHealth) error {
	r.called = true
	if err := ctx.Err(); err != nil {
		r.t.Fatalf("健康写入继承已取消预算：%v", err)
	}
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > time.Second || time.Until(deadline) <= 0 {
		r.t.Fatal("健康记录缺少独立短预算")
	}
	if health.ConnectionRevision != 3 || health.Status != HealthStatusUnhealthy {
		r.t.Fatalf("结果上下文丢失：%+v", health)
	}
	return nil
}

func TestProbeCanRecordExpiredCheckWithoutExtendingUpstream(t *testing.T) {
	recorder := &checkingHealthRecorder{t: t}
	prober, err := NewProber(newTestClient(t, Options{}), recorder)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("测试预算应已过期")
	}
	result, err := prober.Probe(ctx, Target{
		ChannelID: 1, ConnectionRevision: 3, BaseURL: "http://127.0.0.1:1/v1",
		Protocol: store.ChannelProtocolOpenAIChat, APIKey: "test-only-key",
	}, "test-model")
	if err != nil || !recorder.called || result.ErrorCode != "timeout" {
		t.Fatalf("检查超时结果丢失：%+v err=%v recorded=%t", result, err, recorder.called)
	}
}
