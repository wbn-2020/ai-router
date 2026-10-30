package adminauth

import (
	"sync"
	"time"
)

// limiterPruneThreshold 是触发过期条目清理的条目数阈值。
//
// 单个管理员场景下正常只有极少数来源；阈值用来防止伪造来源导致内存无限增长。
const limiterPruneThreshold = 1024

// loginFailure 记录同一来源在滑动窗口内的尝试时间戳（升序，最多 maxFailures 条）。
type loginFailure struct {
	attempts []time.Time
}

// loginLimiter 实现“同一来源 15 分钟内最多 5 次尝试”的登录限流。
//
// 采用滑动窗口：任意 15 分钟区间内尝试次数都不会超过上限，避免固定窗口在
// 边界处被绕过。尝试在密码校验前原子占位，并发请求也不能绕过上限；成功登录
// 删除该来源的全部记录。所有时间由调用方传入，既保证一次登录流程内时间一致，
// 也让测试无需真实等待。
type loginLimiter struct {
	mu          sync.Mutex
	window      time.Duration
	maxFailures int
	entries     map[string]loginFailure
}

func newLoginLimiter(window time.Duration, maxFailures int) *loginLimiter {
	return &loginLimiter{
		window:      window,
		maxFailures: maxFailures,
		entries:     make(map[string]loginFailure),
	}
}

// beginAttempt 原子地检查窗口并占用一次尝试名额。
//
// 返回 false 表示来源已达到上限，调用方必须直接拒绝，不再校验密码。
func (l *loginLimiter) beginAttempt(source string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	active := l.activeAttempts(l.entries[source], now)
	if len(active) >= l.maxFailures {
		l.entries[source] = loginFailure{attempts: active}
		return false
	}

	active = append(active, now)
	l.entries[source] = loginFailure{attempts: active}
	l.pruneLocked(now)
	return true
}

// recordSuccess 清除来源的全部尝试记录。
func (l *loginLimiter) recordSuccess(source string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, source)
}

// activeAttempts 返回窗口内的尝试时间戳；调用方必须持有锁。
func (l *loginLimiter) activeAttempts(entry loginFailure, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	active := entry.attempts[:0]
	for _, attemptedAt := range entry.attempts {
		if attemptedAt.After(cutoff) {
			active = append(active, attemptedAt)
		}
	}
	return active
}

// pruneLocked 在条目数超过阈值时清理过期条目；调用方必须持有锁。
func (l *loginLimiter) pruneLocked(now time.Time) {
	if len(l.entries) <= limiterPruneThreshold {
		return
	}
	for source, entry := range l.entries {
		if len(l.activeAttempts(entry, now)) == 0 {
			delete(l.entries, source)
		}
	}
}
