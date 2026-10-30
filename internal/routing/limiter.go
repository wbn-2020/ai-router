package routing

import "sync"

// Limiter 维护每个渠道的进程内并发槽位。
//
// 槽位以带缓冲的 chan struct{} 表示：容量等于渠道的 max_concurrency，
// 每写入一个 token 表示占用一个槽位，读取一个 token 表示释放。Limiter
// 只存在于当前进程；第一版不提供跨进程/多实例共享计数。
//
// Limiter 可被并发使用。所有方法都只做 O(1) 的短临界区操作，不执行 I/O。
type Limiter struct {
	mu       sync.Mutex
	channels map[int64]*channelSemaphore
}

// channelSemaphore 是单个渠道的信号量。
//
// target 是当前期望上限；limit 是底层 token 通道的容量。管理员调低
// max_concurrency 时，target 立即生效（不再发放新槽位），底层通道等已占用
// 槽位释放后再收缩，避免已获得的 *Slot 无法归还。
type channelSemaphore struct {
	limit  int
	target int
	slots  chan struct{}
}

// NewLimiter 构造空的进程内并发限制器。
func NewLimiter() *Limiter {
	return &Limiter{channels: make(map[int64]*channelSemaphore)}
}

// TryAcquire 尝试为非负渠道 ID 获取一个并发槽位。
//
// limit 是渠道当前的 max_concurrency；小于 1 时按 1 处理。成功返回的
// *Slot 必须由调用方释放（通常 defer slot.Release()）。失败表示该渠道
// 已达到并发上限，调用方应尝试下一个候选渠道。
func (l *Limiter) TryAcquire(channelID int64, limit int) (*Slot, bool) {
	if l == nil {
		return nil, false
	}
	if limit < 1 {
		limit = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.channels == nil {
		l.channels = make(map[int64]*channelSemaphore)
	}
	semaphore := l.channels[channelID]
	if semaphore == nil {
		semaphore = &channelSemaphore{
			limit:  limit,
			target: limit,
			slots:  make(chan struct{}, limit),
		}
		l.channels[channelID] = semaphore
	} else {
		semaphore.resize(limit)
	}

	if len(semaphore.slots) >= semaphore.target {
		return nil, false
	}
	// target <= cap(semaphore.slots)，此处写入不会阻塞。
	semaphore.slots <- struct{}{}
	return &Slot{limiter: l, channelID: channelID}, true
}

// InUse 返回渠道当前占用的槽位数，主要用于观测与测试。
func (l *Limiter) InUse(channelID int64) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	semaphore := l.channels[channelID]
	if semaphore == nil {
		return 0
	}
	return len(semaphore.slots)
}

// resize 应用新的上限。扩容时保留已占用 token；缩容只更新 target。
func (s *channelSemaphore) resize(limit int) {
	s.target = limit
	if limit <= s.limit {
		return
	}

	grown := make(chan struct{}, limit)
	for len(s.slots) > 0 {
		<-s.slots
		grown <- struct{}{}
	}
	s.slots = grown
	s.limit = limit
}

// release 归还一个槽位。重复释放由 Slot 的 sync.Once 挡住；这里额外容忍
// 空信号量，避免竞态或重复调用造成负数计数。
func (l *Limiter) release(channelID int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	semaphore := l.channels[channelID]
	if semaphore == nil {
		return
	}
	select {
	case <-semaphore.slots:
	default:
		return
	}

	// 缩容后 len <= target 时把底层通道收缩到 target，恢复紧凑容量。
	if semaphore.target < semaphore.limit && len(semaphore.slots) <= semaphore.target {
		shrunk := make(chan struct{}, semaphore.target)
		for len(semaphore.slots) > 0 {
			<-semaphore.slots
			shrunk <- struct{}{}
		}
		semaphore.slots = shrunk
		semaphore.limit = semaphore.target
	}
}

// Slot 是一次成功的并发槽位占用。
//
// Slot 只能通过 Limiter.TryAcquire 获得；Release 可以重复调用，但只有第一次
// 生效。调用方应使用 defer 确保所有路径（正常、错误、流式取消）都释放槽位。
type Slot struct {
	limiter   *Limiter
	channelID int64
	once      sync.Once
}

// Release 归还槽位。nil 或重复调用都是安全的 no-op。
func (s *Slot) Release() {
	if s == nil || s.limiter == nil {
		return
	}
	s.once.Do(func() {
		s.limiter.release(s.channelID)
	})
}

// ChannelID 返回槽位所属渠道。
func (s *Slot) ChannelID() int64 {
	if s == nil {
		return 0
	}
	return s.channelID
}
