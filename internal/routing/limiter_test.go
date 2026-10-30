package routing

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustAcquire(t *testing.T, limiter *Limiter, channelID int64, limit int) *Slot {
	t.Helper()
	slot, ok := limiter.TryAcquire(channelID, limit)
	if !ok {
		t.Fatalf("TryAcquire(channel=%d, limit=%d) 失败，期望成功", channelID, limit)
	}
	if slot == nil {
		t.Fatal("TryAcquire 返回 nil Slot")
	}
	return slot
}

// TestLimiterTryAcquireRespectsLimit 覆盖上限、重复释放与槽位回收。
func TestLimiterTryAcquireRespectsLimit(t *testing.T) {
	limiter := NewLimiter()

	first := mustAcquire(t, limiter, 1, 2)
	second := mustAcquire(t, limiter, 1, 2)
	if slot, ok := limiter.TryAcquire(1, 2); ok {
		slot.Release()
		t.Fatal("超过 max_concurrency 仍获取到槽位")
	}
	if got := limiter.InUse(1); got != 2 {
		t.Fatalf("InUse = %d，期望 2", got)
	}

	// 重复释放只生效一次，不能把第二个占用者的槽位也放掉。
	first.Release()
	first.Release()
	if got := limiter.InUse(1); got != 1 {
		t.Fatalf("重复释放后 InUse = %d，期望 1", got)
	}

	// 重复释放只腾出一个槽位：容量 2 下允许补位一次，随后必须再次满载。
	replacement := mustAcquire(t, limiter, 1, 2)
	if slot, ok := limiter.TryAcquire(1, 2); ok {
		slot.Release()
		t.Fatal("两个槽位均已占用时仍获取到槽位")
	}

	second.Release()
	replacement.Release()
	if got := limiter.InUse(1); got != 0 {
		t.Fatalf("全部释放后 InUse = %d，期望 0", got)
	}
	third := mustAcquire(t, limiter, 1, 2)
	third.Release()
	if got := limiter.InUse(1); got != 0 {
		t.Fatalf("最终 InUse = %d，期望 0", got)
	}
}

// TestLimiterIsolatesChannels 覆盖不同渠道的槽位互不影响。
func TestLimiterIsolatesChannels(t *testing.T) {
	limiter := NewLimiter()
	alpha := mustAcquire(t, limiter, 10, 1)
	beta := mustAcquire(t, limiter, 20, 1)

	if slot, ok := limiter.TryAcquire(10, 1); ok {
		slot.Release()
		t.Fatal("渠道 10 已满但仍获取到槽位")
	}
	if slot, ok := limiter.TryAcquire(20, 1); ok {
		slot.Release()
		t.Fatal("渠道 20 已满但仍获取到槽位")
	}

	alpha.Release()
	alphaSecond := mustAcquire(t, limiter, 10, 1)
	alphaSecond.Release()
	if got := limiter.InUse(20); got != 1 {
		t.Fatalf("渠道 20 InUse = %d，期望仍为 1", got)
	}
	beta.Release()

	if alpha.ChannelID() != 10 {
		t.Fatalf("Slot.ChannelID = %d，期望 10", alpha.ChannelID())
	}
}

// TestLimiterAppliesChangedLimit 覆盖管理员调大/调小 max_concurrency。
func TestLimiterAppliesChangedLimit(t *testing.T) {
	limiter := NewLimiter()

	first := mustAcquire(t, limiter, 7, 2)
	second := mustAcquire(t, limiter, 7, 2)

	// 调低到 1：已占用不回收，但不再发放新槽位。
	if slot, ok := limiter.TryAcquire(7, 1); ok {
		slot.Release()
		t.Fatal("调低上限后仍超发槽位")
	}
	first.Release()
	if slot, ok := limiter.TryAcquire(7, 1); ok {
		slot.Release()
		t.Fatal("仍有一个占用时调低上限后超发")
	}
	second.Release()

	// 占用清空后，新上限 1 应正常工作，然后调大到 3 立即生效。
	one := mustAcquire(t, limiter, 7, 1)
	if slot, ok := limiter.TryAcquire(7, 1); ok {
		slot.Release()
		t.Fatal("上限为 1 时不应额外发放槽位")
	}
	one.Release()

	one = mustAcquire(t, limiter, 7, 1)
	// 用新上限 3 再取两个槽位：扩容保留已占用的一个。
	two := mustAcquire(t, limiter, 7, 3)
	three := mustAcquire(t, limiter, 7, 3)
	if slot, ok := limiter.TryAcquire(7, 3); ok {
		slot.Release()
		t.Fatal("扩容到 3 后发放了第 4 个槽位")
	}
	if got := limiter.InUse(7); got != 3 {
		t.Fatalf("InUse = %d，期望 3", got)
	}
	one.Release()
	two.Release()
	three.Release()
	if got := limiter.InUse(7); got != 0 {
		t.Fatalf("最终 InUse = %d，期望 0", got)
	}
}

// TestLimiterConcurrentTryAcquire 验证并发路径不会超过 max_concurrency，
// 且全部槽位最终归还。
func TestLimiterConcurrentTryAcquire(t *testing.T) {
	const (
		limit   = 3
		workers = 24
	)
	limiter := NewLimiter()

	var active atomic.Int32
	var peak atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				slot, ok := limiter.TryAcquire(99, limit)
				if !ok {
					time.Sleep(200 * time.Microsecond)
					continue
				}
				current := active.Add(1)
				for {
					previous := peak.Load()
					if current <= previous || peak.CompareAndSwap(previous, current) {
						break
					}
				}
				time.Sleep(500 * time.Microsecond)
				active.Add(-1)
				slot.Release()
				return
			}
		}()
	}
	wg.Wait()

	if got := peak.Load(); got > limit {
		t.Fatalf("并发峰值 = %d，超过 max_concurrency=%d", got, limit)
	}
	if got := limiter.InUse(99); got != 0 {
		t.Fatalf("所有请求结束后 InUse = %d，期望 0（槽位泄漏）", got)
	}
}

// TestLimiterNilSafety 覆盖 nil Limiter/Slot 的 no-op 语义。
func TestLimiterNilSafety(t *testing.T) {
	var limiter *Limiter
	if slot, ok := limiter.TryAcquire(1, 1); ok || slot != nil {
		t.Fatal("nil Limiter 不应返回槽位")
	}
	if got := limiter.InUse(1); got != 0 {
		t.Fatalf("nil Limiter.InUse = %d", got)
	}

	var slot *Slot
	slot.Release()
	if slot.ChannelID() != 0 {
		t.Fatal("nil Slot.ChannelID 应为 0")
	}
}
