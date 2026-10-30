// Package routing 实现数据面的候选渠道调度：按入站协议与客户端模型查询
// 启用映射，过滤禁用与熔断渠道，按固定优先级排序，并在进程内为每个渠道
// 维护并发槽位与熔断窗口。
//
// 边界（见计划 T19）：
//   - 只做原生协议匹配，不做 Responses↔Chat↔Anthropic 转换；
//   - 候选顺序为 route.priority ASC → channel.priority ASC →
//     last_used_at ASC（NULL 最先）→ route.id ASC，不引入权重或随机；
//   - 运行态状态（并发计数、熔断窗口、last_error）只存在于当前进程；
//     第一版不依赖 Redis，也不提供多实例共享限流；
//   - 渠道配置与 channels.last_used_at 通过 store 仓储读写，本包不直接
//     执行 SQL，也不接触上游 Key。
package routing

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ai-router/internal/store"
)

const (
	// DefaultCapacityWait 是全部候选渠道满载时的最长等待时间。
	DefaultCapacityWait = 2 * time.Second
	// DefaultCapacityPoll 是等待槽位时的轮询间隔。
	DefaultCapacityPoll = 25 * time.Millisecond

	// DefaultAuthCircuitWindow 是 401/403 触发的熔断窗口。
	DefaultAuthCircuitWindow = 5 * time.Minute
	// DefaultRateLimitCircuitWindow 是 429 触发的熔断窗口。
	DefaultRateLimitCircuitWindow = 10 * time.Second
	// DefaultServerErrorCircuitWindow 是 5xx（含 529）触发的熔断窗口。
	DefaultServerErrorCircuitWindow = 20 * time.Second
)

// 稳定的调度错误。调用方（gateway）只依赖 errors.Is 判断并映射到入站协议
// 的错误 envelope，不解析错误文本。
var (
	// ErrNoCandidates 表示没有启用、协议匹配且未熔断的候选渠道。
	ErrNoCandidates = errors.New("routing: 没有可用候选渠道")
	// ErrChannelCapacity 表示所有候选渠道都已达到 max_concurrency，
	// 且等待超过 CapacityWait。
	ErrChannelCapacity = errors.New("routing: 渠道并发容量已满")
)

// RouteCatalog 是候选查询需要的映射读取能力；store.ModelRoutes 满足该接口。
type RouteCatalog interface {
	ListByModelAndProtocol(ctx context.Context, clientModel, protocol string) ([]store.ModelRoute, error)
}

// ChannelCatalog 是读取渠道配置的能力；store.Channels 满足该接口。
type ChannelCatalog interface {
	Get(ctx context.Context, id int64) (store.Channel, error)
}

// LastUsedRecorder 记录渠道最近被调度的时间；store.Channels 满足该接口。
type LastUsedRecorder interface {
	TouchLastUsed(ctx context.Context, id int64, at time.Time) error
}

var (
	_ RouteCatalog     = (*store.ModelRoutes)(nil)
	_ ChannelCatalog   = (*store.Channels)(nil)
	_ LastUsedRecorder = (*store.Channels)(nil)
)

// CircuitOptions 是内存熔断窗口。零值使用默认窗口；负值同样回落到默认值。
type CircuitOptions struct {
	// AuthWindow 是 401/403 的熔断时长。
	AuthWindow time.Duration
	// RateLimitWindow 是 429 的熔断时长。
	RateLimitWindow time.Duration
	// ServerErrorWindow 是 5xx 的熔断时长。
	ServerErrorWindow time.Duration
}

// Options 是 Scheduler 的构造参数。
//
// Routes 与 Channels 必须非 nil；LastUsed 为 nil 时不更新 last_used_at
// （只应在测试或纯内存场景使用）。CapacityWait/CapacityPoll 为零时使用
// 默认值；Clock 为零时使用 time.Now。
type Options struct {
	Routes   RouteCatalog
	Channels ChannelCatalog
	LastUsed LastUsedRecorder
	Clock    func() time.Time
	Circuit  CircuitOptions
	// CapacityWait 是全部候选满载时的最长等待时间。
	CapacityWait time.Duration
	// CapacityPoll 是等待槽位时的轮询间隔；大于 CapacityWait 时按等待时间截断。
	CapacityPoll time.Duration
}

// Scheduler 负责候选查询、排序、并发槽位与熔断状态。
//
// Scheduler 可被并发使用。它不修改渠道配置，只通过 LastUsedRecorder 写入
// last_used_at；并发与熔断状态在进程内维护。
type Scheduler struct {
	routes   RouteCatalog
	channels ChannelCatalog
	lastUsed LastUsedRecorder
	limiter  *Limiter
	breaker  *circuitBreaker

	capacityWait time.Duration
	capacityPoll time.Duration
	now          func() time.Time
}

// Candidate 是一条可参与调度的映射及其渠道配置。
//
// Channel 是候选查询时的快照；同一请求的重试仍应向 store 重新取候选，
// 以便看到管理员刚刚修改的启停与优先级。
type Candidate struct {
	Route   store.ModelRoute
	Channel store.Channel
}

// New 构造调度器。Routes/Channels 缺失时返回错误。
func New(opts Options) (*Scheduler, error) {
	if opts.Routes == nil {
		return nil, errors.New("routing: Routes 不能为空")
	}
	if opts.Channels == nil {
		return nil, errors.New("routing: Channels 不能为空")
	}

	now := opts.Clock
	if now == nil {
		now = time.Now
	}
	capacityWait := opts.CapacityWait
	if capacityWait <= 0 {
		capacityWait = DefaultCapacityWait
	}
	capacityPoll := opts.CapacityPoll
	if capacityPoll <= 0 {
		capacityPoll = DefaultCapacityPoll
	}
	if capacityPoll > capacityWait {
		capacityPoll = capacityWait
	}

	return &Scheduler{
		routes:       opts.Routes,
		channels:     opts.Channels,
		lastUsed:     opts.LastUsed,
		limiter:      NewLimiter(),
		breaker:      newCircuitBreaker(opts.Circuit, now),
		capacityWait: capacityWait,
		capacityPoll: capacityPoll,
		now:          now,
	}, nil
}

// Candidates 返回某客户端模型在指定入站协议下的可用候选，已按
// route.priority → channel.priority → last_used_at（NULL 最先）→ route.id
// 排序。
//
// 过滤条件：映射启用、渠道启用、渠道协议与入站协议一致、渠道未熔断。
// 容量不在这里过滤：并发槽位由 Acquire 原子获取，避免“查询时可用、使用
// 时已满”的竞态；满载渠道会在 Acquire 中被跳过。
func (s *Scheduler) Candidates(
	ctx context.Context,
	clientModel, protocol string,
) ([]Candidate, error) {
	if s == nil || s.routes == nil || s.channels == nil {
		return nil, errors.New("routing: 调度器未初始化")
	}

	clientModel = strings.TrimSpace(clientModel)
	protocol = strings.TrimSpace(protocol)
	if clientModel == "" || protocol == "" {
		return []Candidate{}, nil
	}

	routes, err := s.routes.ListByModelAndProtocol(ctx, clientModel, protocol)
	if err != nil {
		return nil, fmt.Errorf("routing: 查询候选渠道失败：%w", err)
	}

	candidates := make([]Candidate, 0, len(routes))
	seen := make(map[int64]struct{}, len(routes))
	for _, route := range routes {
		// 仓储查询已经过滤了这些条件；这里再校验一次，保证任何
		// RouteCatalog 实现都不能把禁用/协议不匹配的渠道送进调度。
		if !route.Enabled {
			continue
		}
		channel, err := s.channels.Get(ctx, route.ChannelID)
		if errors.Is(err, store.ErrChannelNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("routing: 读取渠道 %d 失败：%w", route.ChannelID, err)
		}
		if !channel.Enabled || channel.Protocol != protocol {
			continue
		}
		if !s.breaker.allow(channel.ID) {
			continue
		}
		if _, duplicate := seen[channel.ID]; duplicate {
			continue
		}
		seen[channel.ID] = struct{}{}
		candidates = append(candidates, Candidate{Route: route, Channel: channel})
	}

	sortCandidates(candidates)
	return candidates, nil
}

// Acquire 按候选顺序选择第一个有空闲并发槽位的渠道，并返回占用的槽位。
//
// 渠道满载时先尝试下一个候选；全部候选都满载时每 CapacityPoll 重新尝试，
// 最多等待 CapacityWait。等待期间获得槽位则继续请求；超时返回
// ErrChannelCapacity；客户端取消返回 ctx.Err()。没有可用候选时返回
// ErrNoCandidates，不等待。
//
// 成功获得槽位后会以 best-effort 方式更新该渠道的 last_used_at；写入失败
// 不阻断请求（LRU 只影响同优先级渠道的相对顺序）。调用方必须在使用完
// 渠道后调用 slot.Release()，包括流式取消路径。
func (s *Scheduler) Acquire(
	ctx context.Context,
	clientModel, protocol string,
) (Candidate, *Slot, error) {
	return s.AcquireExcluding(ctx, clientModel, protocol, nil)
}

// AcquireExcluding 与 Acquire 相同，但跳过 exclude 中的渠道。
//
// gateway 的失败切换用它保证“同一渠道不重试”：每次失败后把渠道加入
// exclude，再重新获取槽位。exclude 只读，调用方不得在调用期间修改。
func (s *Scheduler) AcquireExcluding(
	ctx context.Context,
	clientModel, protocol string,
	exclude map[int64]struct{},
) (Candidate, *Slot, error) {
	if s == nil || s.limiter == nil {
		return Candidate{}, nil, errors.New("routing: 调度器未初始化")
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, nil, err
	}

	candidates, err := s.Candidates(ctx, clientModel, protocol)
	if err != nil {
		return Candidate{}, nil, err
	}
	if len(exclude) > 0 {
		filtered := candidates[:0]
		for _, candidate := range candidates {
			if _, skip := exclude[candidate.Channel.ID]; skip {
				continue
			}
			filtered = append(filtered, candidate)
		}
		candidates = filtered
	}
	if len(candidates) == 0 {
		return Candidate{}, nil, ErrNoCandidates
	}

	// 等待期间复用同一份候选快照：满载时每 25ms 查询 SQLite 会放大数据库
	// 压力；槽位释放后按原排序继续即可。熔断状态仍在每次尝试前重新检查。
	deadline := time.NewTimer(s.capacityWait)
	defer deadline.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return Candidate{}, nil, err
		}
		for _, candidate := range candidates {
			// 等待期间熔断状态可能变化；每次尝试前重新检查。
			if !s.breaker.allow(candidate.Channel.ID) {
				continue
			}
			slot, acquired := s.limiter.TryAcquire(
				candidate.Channel.ID, candidate.Channel.MaxConcurrency)
			if !acquired {
				continue
			}
			s.touchLastUsed(ctx, candidate.Channel.ID)
			return candidate, slot, nil
		}

		poll := time.NewTimer(s.capacityPoll)
		select {
		case <-ctx.Done():
			poll.Stop()
			return Candidate{}, nil, ctx.Err()
		case <-deadline.C:
			poll.Stop()
			return Candidate{}, nil, fmt.Errorf(
				"%w：%d 个候选渠道均无空闲并发槽位", ErrChannelCapacity, len(candidates))
		case <-poll.C:
		}
	}
}

// RecordSuccess 清除渠道的熔断状态与 last_error。
func (s *Scheduler) RecordSuccess(channelID int64) {
	if s == nil || s.breaker == nil {
		return
	}
	s.breaker.succeed(channelID)
}

// RecordFailure 记录一次上游失败并按状态码打开熔断。
//
// 401/403 打开 5m，429 打开 10s，5xx（含 529）打开 20s；其他状态码
// （例如 400）不熔断。message 必须是已脱敏的短文案，调用方不得传入上游
// 响应 body、Key 或完整 URL。返回值表示是否触发了熔断。
func (s *Scheduler) RecordFailure(channelID int64, status int, message string) bool {
	if s == nil || s.breaker == nil {
		return false
	}
	return s.breaker.fail(channelID, status, message)
}

// CircuitSnapshot 是渠道熔断状态的内存快照。
type CircuitSnapshot struct {
	// Open 表示当前时间点熔断是否仍然打开。
	Open bool
	// OpenUntil 是本次熔断的截止时间；未熔断为零值。
	OpenUntil time.Time
	// LastError 是最近一次触发熔断的脱敏错误文案。
	LastError string
}

// Circuit 返回渠道当前的熔断快照，供观测与测试使用。
func (s *Scheduler) Circuit(channelID int64) CircuitSnapshot {
	if s == nil || s.breaker == nil {
		return CircuitSnapshot{}
	}
	return s.breaker.snapshot(channelID)
}

// touchLastUsed 记录渠道最近被调度的时间。失败只影响 LRU 的精度，
// 不影响本次请求。
func (s *Scheduler) touchLastUsed(ctx context.Context, channelID int64) {
	if s.lastUsed == nil {
		return
	}
	_ = s.lastUsed.TouchLastUsed(ctx, channelID, s.now())
}

// sortCandidates 按固定优先级排序，不引入权重或随机。
func sortCandidates(candidates []Candidate) {
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.Route.Priority != right.Route.Priority {
			return left.Route.Priority < right.Route.Priority
		}
		if left.Channel.Priority != right.Channel.Priority {
			return left.Channel.Priority < right.Channel.Priority
		}
		if cmp := compareLastUsed(left.Channel.LastUsedAt, right.Channel.LastUsedAt); cmp != 0 {
			return cmp < 0
		}
		return left.Route.ID < right.Route.ID
	})
}

// compareLastUsed 比较 last_used_at；nil（从未使用）排在最前。
func compareLastUsed(left, right *time.Time) int {
	switch {
	case left == nil && right == nil:
		return 0
	case left == nil:
		return -1
	case right == nil:
		return 1
	case left.Before(*right):
		return -1
	case right.Before(*left):
		return 1
	default:
		return 0
	}
}

// circuitBreaker 是进程内熔断状态。
type circuitBreaker struct {
	mu     sync.Mutex
	states map[int64]circuitState
	now    func() time.Time
	auth   time.Duration
	rate   time.Duration
	server time.Duration
}

type circuitState struct {
	openUntil time.Time
	lastError string
}

// newCircuitBreaker 构造熔断器；窗口为零或负值时使用默认值。
func newCircuitBreaker(opts CircuitOptions, now func() time.Time) *circuitBreaker {
	if now == nil {
		now = time.Now
	}
	auth := opts.AuthWindow
	if auth <= 0 {
		auth = DefaultAuthCircuitWindow
	}
	rate := opts.RateLimitWindow
	if rate <= 0 {
		rate = DefaultRateLimitCircuitWindow
	}
	server := opts.ServerErrorWindow
	if server <= 0 {
		server = DefaultServerErrorCircuitWindow
	}
	return &circuitBreaker{
		states: make(map[int64]circuitState),
		now:    now,
		auth:   auth,
		rate:   rate,
		server: server,
	}
}

// allow 报告渠道当前是否允许调度；过期的熔断窗口会被清除。
func (b *circuitBreaker) allow(channelID int64) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[channelID]
	if !ok || state.openUntil.IsZero() {
		return true
	}
	if b.now().Before(state.openUntil) {
		return false
	}
	state.openUntil = time.Time{}
	b.states[channelID] = state
	return true
}

// succeed 清除渠道的熔断状态与 last_error。
func (b *circuitBreaker) succeed(channelID int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.states, channelID)
}

// fail 按状态码打开熔断；返回状态码是否属于熔断范围。
func (b *circuitBreaker) fail(channelID int64, status int, message string) bool {
	if b == nil {
		return false
	}
	window, tripped := b.windowFor(status)
	if !tripped {
		return false
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.states[channelID] = circuitState{
		openUntil: b.now().Add(window),
		lastError: message,
	}
	return true
}

func (b *circuitBreaker) windowFor(status int) (time.Duration, bool) {
	switch {
	case status == 401 || status == 403:
		return b.auth, true
	case status == 429:
		return b.rate, true
	case status >= 500 && status <= 599:
		return b.server, true
	default:
		return 0, false
	}
}

func (b *circuitBreaker) snapshot(channelID int64) CircuitSnapshot {
	if b == nil {
		return CircuitSnapshot{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	state, ok := b.states[channelID]
	if !ok {
		return CircuitSnapshot{}
	}
	return CircuitSnapshot{
		Open:      !state.openUntil.IsZero() && b.now().Before(state.openUntil),
		OpenUntil: state.openUntil,
		LastError: state.lastError,
	}
}
