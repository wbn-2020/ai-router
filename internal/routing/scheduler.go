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
	// ErrNoCandidates 表示没有启用、协议匹配的候选渠道。
	ErrNoCandidates = errors.New("routing: 没有可用候选渠道")
	// ErrAllCircuitsOpen 表示存在启用且协议匹配的候选，但全部因熔断被排除；
	// 这是暂时性错误，调用方应返回 503 而不是 404。
	ErrAllCircuitsOpen = errors.New("routing: 所有候选渠道均已熔断")
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
	// DisableCapacityWait 为 true 时不做任何等待：所有候选满载立即返回
	// ErrChannelCapacity。用于配置 GATEWAY_CAPACITY_WAIT=0 的快速失败语义。
	DisableCapacityWait bool
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
	noWait       bool
	now          func() time.Time
}

// Candidate 是一条可参与调度的映射及其渠道配置。
//
// Channel 是候选查询时的快照；同一请求的重试仍应向 store 重新取候选，
// 以便看到管理员刚刚修改的启停与优先级。
type Candidate struct {
	Route   store.ModelRoute
	Channel store.Channel
	ticket  *dispatchTicket
}

const (
	FailureScopeRequest = "request"
	FailureScopeModel   = "model"
	FailureScopeChannel = "channel"
	MaxRetryAfter       = 5 * time.Minute
)

// Failure 的 Message 必须是脱敏短文案，不能包含 Key、URL 或上游响应体。
type Failure struct {
	Scope      string
	Status     int
	Message    string
	RetryAfter time.Duration
}

// RuntimeSnapshot 分开呈现渠道级故障与模型局部故障；读取不占恢复许可。
type RuntimeSnapshot struct {
	State           string
	InUse           int
	Limit           int
	OpenUntil       *time.Time
	LastError       string
	ModelCooldowns  int
	ModelRecovering int
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
		noWait:       opts.DisableCapacityWait,
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
	return s.candidates(ctx, clientModel, protocol, true)
}

// candidates 是 Candidates 的实现；respectCircuit 为 false 时忽略熔断状态，
// 仅用于区分“没有配置候选”和“候选全部熔断”。
func (s *Scheduler) candidates(
	ctx context.Context,
	clientModel, protocol string,
	respectCircuit bool,
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
		if respectCircuit && !s.breaker.allow(Candidate{Route: route, Channel: channel}) {
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
// 最多等待 CapacityWait（DisableCapacityWait 时立即返回）。等待期间获得
// 槽位则继续请求；超时返回 ErrChannelCapacity；客户端取消返回 ctx.Err()。
// 没有可用候选时返回 ErrNoCandidates；候选全部熔断时返回
// ErrAllCircuitsOpen，均不等待。
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

	protocol = strings.TrimSpace(protocol)
	candidates, err := s.candidates(ctx, clientModel, protocol, false)
	if err != nil {
		return Candidate{}, nil, err
	}
	filtered := candidates[:0]
	for _, candidate := range candidates {
		if _, skip := exclude[candidate.Channel.ID]; !skip {
			filtered = append(filtered, candidate)
		}
	}
	candidates = filtered
	deadline := time.NewTimer(s.capacityWait)
	defer deadline.Stop()

	for {
		if err := ctx.Err(); err != nil {
			return Candidate{}, nil, err
		}
		capacityBlocked := false
		for i := range candidates {
			candidate := &candidates[i]
			if !candidate.Channel.Enabled {
				continue
			}
			if !s.breaker.allow(*candidate) {
				continue
			}
			slot, acquired := s.limiter.TryAcquire(candidate.Channel.ID, candidate.Channel.MaxConcurrency)
			if !acquired {
				capacityBlocked = true
				continue
			}
			// 仅预留到槽位的候选重新读取配置，避免满载时反复扫描数据库。
			fresh, getErr := s.channels.Get(ctx, candidate.Channel.ID)
			if getErr != nil {
				slot.Release()
				if errors.Is(getErr, store.ErrChannelNotFound) {
					candidate.Channel.Enabled = false
					continue
				}
				return Candidate{}, nil, fmt.Errorf("routing: 复查渠道失败：%w", getErr)
			}
			if !fresh.Enabled || fresh.Protocol != protocol || fresh.ConnectionRevision != candidate.Channel.ConnectionRevision {
				slot.Release()
				candidate.Channel.Enabled = false
				continue
			}
			candidate.Channel = fresh
			if !s.limiter.confirmLimit(fresh.ID, fresh.MaxConcurrency) {
				slot.Release()
				capacityBlocked = true
				continue
			}
			ticket := s.breaker.acquire(*candidate)
			if ticket == nil {
				slot.Release()
				continue
			}
			candidate.ticket = ticket
			slot.onRelease = func() { s.breaker.cancel(ticket) }
			if err := ctx.Err(); err != nil {
				slot.Release()
				return Candidate{}, nil, err
			}
			s.touchLastUsed(ctx, candidate.Channel.ID)
			if err := ctx.Err(); err != nil {
				slot.Release()
				return Candidate{}, nil, err
			}
			return *candidate, slot, nil
		}
		configured := false
		for _, candidate := range candidates {
			configured = configured || candidate.Channel.Enabled
		}
		if !configured {
			return Candidate{}, nil, ErrNoCandidates
		}
		if !capacityBlocked {
			return Candidate{}, nil, ErrAllCircuitsOpen
		}
		if s.noWait {
			return Candidate{}, nil, ErrChannelCapacity
		}
		poll := time.NewTimer(s.capacityPoll)
		select {
		case <-ctx.Done():
			poll.Stop()
			return Candidate{}, nil, ctx.Err()
		case <-deadline.C:
			poll.Stop()
			return Candidate{}, nil, ErrChannelCapacity
		case <-poll.C:
		}
	}
}

// AcquirePinned 为有状态请求取得指定渠道的槽位，不跨渠道回退。
//
// 只在该渠道仍是启用、协议匹配、连接版本未变且有空闲槽位时成功；
// 熔断打开返回 ErrAllCircuitsOpen，满载返回 ErrChannelCapacity。
func (s *Scheduler) AcquirePinned(
	ctx context.Context,
	channelID int64,
	clientModel, protocol string,
) (Candidate, *Slot, error) {
	if s == nil || s.limiter == nil {
		return Candidate{}, nil, errors.New("routing: 调度器未初始化")
	}
	if err := ctx.Err(); err != nil {
		return Candidate{}, nil, err
	}
	candidates, err := s.candidates(ctx, clientModel, strings.TrimSpace(protocol), false)
	if err != nil {
		return Candidate{}, nil, err
	}
	var candidate *Candidate
	for i := range candidates {
		if candidates[i].Channel.ID == channelID {
			candidate = &candidates[i]
			break
		}
	}
	if candidate == nil {
		return Candidate{}, nil, ErrNoCandidates
	}
	if !s.breaker.allow(*candidate) {
		return Candidate{}, nil, ErrAllCircuitsOpen
	}
	slot, acquired := s.limiter.TryAcquire(candidate.Channel.ID, candidate.Channel.MaxConcurrency)
	if !acquired {
		return Candidate{}, nil, ErrChannelCapacity
	}
	fresh, getErr := s.channels.Get(ctx, candidate.Channel.ID)
	if getErr != nil || !fresh.Enabled || fresh.Protocol != strings.TrimSpace(protocol) ||
		fresh.ConnectionRevision != candidate.Channel.ConnectionRevision ||
		!s.limiter.confirmLimit(fresh.ID, fresh.MaxConcurrency) {
		slot.Release()
		if getErr != nil && !errors.Is(getErr, store.ErrChannelNotFound) {
			return Candidate{}, nil, fmt.Errorf("routing: 复查渠道失败：%w", getErr)
		}
		return Candidate{}, nil, ErrNoCandidates
	}
	candidate.Channel = fresh
	ticket := s.breaker.acquire(*candidate)
	if ticket == nil {
		slot.Release()
		return Candidate{}, nil, ErrAllCircuitsOpen
	}
	candidate.ticket = ticket
	slot.onRelease = func() { s.breaker.cancel(ticket) }
	if err := ctx.Err(); err != nil {
		slot.Release()
		return Candidate{}, nil, err
	}
	s.touchLastUsed(ctx, candidate.Channel.ID)
	return *candidate, slot, nil
}

// RecordSuccess 只接受本次派发的票据；必须在 slot.Release 前调用。
func (s *Scheduler) RecordSuccess(candidate Candidate) {
	if s.validFeedback(candidate) {
		s.breaker.finish(candidate.ticket, nil)
	}
}

// RecordFailure 记录局部或渠道故障，返回是否更新冷却；请求错误不熔断。
func (s *Scheduler) RecordFailure(candidate Candidate, failure Failure) bool {
	if !s.validFeedback(candidate) {
		return false
	}
	return s.breaker.finish(candidate.ticket, &failure)
}

func (s *Scheduler) validFeedback(candidate Candidate) bool {
	if s == nil || s.breaker == nil || candidate.ticket == nil || candidate.ticket.owner != s.breaker {
		return false
	}
	ticket := candidate.ticket
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	channel, err := s.channels.Get(ctx, ticket.channelID)
	if err != nil || channel.ConnectionRevision != ticket.revision {
		s.breaker.cancel(ticket)
		return false
	}
	return true
}

// Runtime 返回只读快照；到期但尚未成功恢复仍显示 recovering。
func (s *Scheduler) Runtime(channel store.Channel) RuntimeSnapshot {
	if s == nil || s.breaker == nil {
		return RuntimeSnapshot{State: "ready", Limit: channel.MaxConcurrency}
	}
	snapshot := s.breaker.runtime(channel)
	snapshot.InUse = s.limiter.InUse(channel.ID)
	return snapshot
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

// circuitBreaker 用同一把锁维护两级状态和恢复许可。
type circuitBreaker struct {
	mu     sync.Mutex
	states map[int64]*channelCircuit
	now    func() time.Time
	auth   time.Duration
	rate   time.Duration
	server time.Duration
}

type modelCircuitKey struct {
	protocol string
	model    string
}

type channelCircuit struct {
	revision int64
	channel  circuitState
	models   map[modelCircuitKey]*circuitState
}

type circuitState struct {
	generation uint64
	resolved   uint64
	openUntil  time.Time
	lastError  string
	probe      *dispatchTicket
}

type dispatchTicket struct {
	owner             *circuitBreaker
	channelID         int64
	revision          int64
	model             modelCircuitKey
	channelGeneration uint64
	modelGeneration   uint64
	done              bool // guarded by owner.mu; copies of Candidate share completion
}

func candidateModelKey(candidate Candidate) modelCircuitKey {
	return modelCircuitKey{protocol: candidate.Channel.Protocol, model: candidate.Route.UpstreamModel}
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
		states: make(map[int64]*channelCircuit),
		now:    now,
		auth:   auth,
		rate:   rate,
		server: server,
	}
}

func (state *circuitState) available(now time.Time) bool {
	return state == nil || state.openUntil.IsZero() || (!now.Before(state.openUntil) && state.probe == nil)
}

// allow 不改变状态，也不占恢复许可。
func (b *circuitBreaker) allow(candidate Candidate) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	state := b.states[candidate.Channel.ID]
	if state == nil || state.revision < candidate.Channel.ConnectionRevision {
		return true
	}
	if state.revision != candidate.Channel.ConnectionRevision {
		return false
	}
	now := b.now()
	return state.channel.available(now) && state.models[candidateModelKey(candidate)].available(now)
}

func (b *circuitBreaker) acquire(candidate Candidate) *dispatchTicket {
	b.mu.Lock()
	defer b.mu.Unlock()
	id, revision := candidate.Channel.ID, candidate.Channel.ConnectionRevision
	state := b.states[id]
	if state != nil && state.revision > revision {
		return nil
	}
	if state == nil || state.revision != revision {
		state = &channelCircuit{revision: revision, models: make(map[modelCircuitKey]*circuitState)}
		b.states[id] = state
	}
	key := candidateModelKey(candidate)
	model := state.models[key]
	now := b.now()
	if !state.channel.available(now) || !model.available(now) {
		return nil
	}
	if model == nil {
		model = &circuitState{}
		state.models[key] = model
	}
	ticket := &dispatchTicket{owner: b, channelID: id, revision: revision, model: key,
		channelGeneration: state.channel.generation, modelGeneration: model.generation}
	if !state.channel.openUntil.IsZero() {
		state.channel.probe = ticket
	}
	if !model.openUntil.IsZero() {
		model.probe = ticket
	}
	return ticket
}

func (b *circuitBreaker) cancel(ticket *dispatchTicket) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ticket.done {
		return
	}
	ticket.done = true
	if state := b.states[ticket.channelID]; state != nil && state.revision == ticket.revision {
		releaseProbe(&state.channel, ticket)
		releaseProbe(state.models[ticket.model], ticket)
	}
}

func releaseProbe(state *circuitState, ticket *dispatchTicket) {
	if state != nil && state.probe == ticket {
		state.probe = nil
	}
}

func resolveCircuit(state *circuitState, generation uint64, ticket *dispatchTicket) {
	if state.generation != generation || (!state.openUntil.IsZero() && state.probe != ticket) {
		return
	}
	if !state.openUntil.IsZero() {
		state.generation++
		state.resolved = state.generation
	}
	state.openUntil = time.Time{}
	state.lastError = ""
	state.probe = nil
}

func (b *circuitBreaker) finish(ticket *dispatchTicket, failure *Failure) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if ticket.done {
		return false
	}
	ticket.done = true
	state := b.states[ticket.channelID]
	if state == nil || state.revision != ticket.revision {
		return false
	}
	model := state.models[ticket.model]
	defer releaseProbe(&state.channel, ticket)
	defer releaseProbe(model, ticket)
	if failure == nil {
		// 任一级出现更新的故障，都不能让较早派发的成功清除状态。
		if state.channel.generation == ticket.channelGeneration && model.generation == ticket.modelGeneration {
			resolveCircuit(&state.channel, ticket.channelGeneration, ticket)
			resolveCircuit(model, ticket.modelGeneration, ticket)
		}
		return false
	}
	if failure.Scope == FailureScopeRequest {
		return false
	}
	window, trips := b.windowFor(failure.Status)
	target, generation := &state.channel, ticket.channelGeneration
	if failure.Scope == FailureScopeModel {
		target, generation = model, ticket.modelGeneration
		if !trips {
			window, trips = b.server, true
		}
	}
	// 已经由恢复请求确认过的旧尝试，不能重新打开状态。
	if !trips || generation < target.resolved || ticket.channelGeneration < state.channel.resolved {
		return false
	}
	if failure.RetryAfter > 0 {
		window = min(failure.RetryAfter, MaxRetryAfter)
	}
	until := b.now().Add(window)
	if until.After(target.openUntil) {
		target.openUntil = until
	}
	target.generation++
	target.lastError = failure.Message
	target.probe = nil
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
	case status <= 0:
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
		Open:      !state.channel.openUntil.IsZero() && b.now().Before(state.channel.openUntil),
		OpenUntil: state.channel.openUntil,
		LastError: state.channel.lastError,
	}
}

func circuitPhase(state *circuitState, now time.Time) string {
	if state == nil || state.openUntil.IsZero() {
		return "ready"
	}
	if now.Before(state.openUntil) {
		return "cooldown"
	}
	return "recovering"
}

func (b *circuitBreaker) runtime(channel store.Channel) RuntimeSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	snapshot := RuntimeSnapshot{State: "ready", Limit: channel.MaxConcurrency}
	state := b.states[channel.ID]
	if state == nil || state.revision != channel.ConnectionRevision {
		return snapshot
	}
	now := b.now()
	snapshot.State = circuitPhase(&state.channel, now)
	snapshot.LastError = state.channel.lastError
	if !state.channel.openUntil.IsZero() {
		until := state.channel.openUntil
		snapshot.OpenUntil = &until
	}
	for _, model := range state.models {
		switch circuitPhase(model, now) {
		case "cooldown":
			snapshot.ModelCooldowns++
		case "recovering":
			snapshot.ModelRecovering++
		}
	}
	return snapshot
}
