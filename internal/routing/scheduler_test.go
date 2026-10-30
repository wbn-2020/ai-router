package routing

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"ai-router/internal/store"
)

// newRoutingTestStore 返回迁移完成的临时 SQLite Store。
func newRoutingTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(store.Paths{
		DataDir: dir,
		DBPath:  filepath.Join(dir, "gateway.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = s.Close()
	})
	if _, err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return s
}

// createRoutingChannel 通过渠道仓储创建测试渠道。调度只使用配置与
// last_used_at，不接触 Key 明文，因此测试密文只需满足非空约束。
func createRoutingChannel(
	t *testing.T,
	repo *store.Channels,
	name, protocol string,
	priority, maxConcurrency int,
	enabled bool,
) store.Channel {
	t.Helper()
	channel, err := repo.Create(context.Background(), store.ChannelInput{
		ChannelConfig: store.ChannelConfig{
			Name:           name,
			BaseURL:        "https://upstream.example.com/v1",
			Protocol:       protocol,
			Priority:       priority,
			MaxConcurrency: maxConcurrency,
			Enabled:        enabled,
		},
		KeyCiphertext: "v1:test:" + name,
		KeyMask:       "sk-****" + name,
	})
	if err != nil {
		t.Fatalf("创建渠道 %s: %v", name, err)
	}
	return channel
}

func createRoutingRoute(
	t *testing.T,
	repo *store.ModelRoutes,
	clientModel string,
	channelID int64,
	priority int,
	enabled bool,
) store.ModelRoute {
	t.Helper()
	route, err := repo.Create(context.Background(), store.ModelRouteInput{
		ClientModel:   clientModel,
		ChannelID:     channelID,
		UpstreamModel: clientModel + "-upstream",
		Priority:      priority,
		Enabled:       enabled,
	})
	if err != nil {
		t.Fatalf("创建映射 %s/%d: %v", clientModel, channelID, err)
	}
	return route
}

func newStoreScheduler(t *testing.T, s *store.Store, opts Options) *Scheduler {
	t.Helper()
	opts.Routes = s.ModelRoutes()
	opts.Channels = s.Channels()
	opts.LastUsed = s.Channels()
	scheduler, err := New(opts)
	if err != nil {
		t.Fatalf("routing.New: %v", err)
	}
	return scheduler
}

func assertCandidateRouteIDs(t *testing.T, candidates []Candidate, want ...int64) {
	t.Helper()
	if len(candidates) != len(want) {
		t.Fatalf("候选数量 = %d，期望 %d：%+v", len(candidates), len(want), candidates)
	}
	for i := range want {
		if candidates[i].Route.ID != want[i] {
			t.Errorf("候选[%d].Route.ID = %d，期望 %d", i, candidates[i].Route.ID, want[i])
		}
	}
}

// TestNewSchedulerDefaultsAndValidation 覆盖构造校验、默认窗口与等待参数。
func TestNewSchedulerDefaultsAndValidation(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("缺少 Routes/Channels 时 New 应失败")
	}
	if _, err := New(Options{Routes: &fakeRouteCatalog{}}); err == nil {
		t.Fatal("缺少 Channels 时 New 应失败")
	}

	scheduler, err := New(Options{
		Routes:   &fakeRouteCatalog{},
		Channels: &fakeChannelCatalog{},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if scheduler.capacityWait != DefaultCapacityWait {
		t.Errorf("capacityWait = %v，期望 %v", scheduler.capacityWait, DefaultCapacityWait)
	}
	if scheduler.capacityPoll != DefaultCapacityPoll {
		t.Errorf("capacityPoll = %v，期望 %v", scheduler.capacityPoll, DefaultCapacityPoll)
	}
	if scheduler.breaker.auth != DefaultAuthCircuitWindow ||
		scheduler.breaker.rate != DefaultRateLimitCircuitWindow ||
		scheduler.breaker.server != DefaultServerErrorCircuitWindow {
		t.Error("熔断默认窗口与常量不一致")
	}

	short, err := New(Options{
		Routes:       &fakeRouteCatalog{},
		Channels:     &fakeChannelCatalog{},
		CapacityWait: 10 * time.Millisecond,
		CapacityPoll: time.Second,
		Circuit: CircuitOptions{
			AuthWindow:        time.Minute,
			RateLimitWindow:   time.Second,
			ServerErrorWindow: 2 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("New(short): %v", err)
	}
	if short.capacityPoll != 10*time.Millisecond {
		t.Errorf("capacityPoll = %v，期望被 CapacityWait 截断为 10ms", short.capacityPoll)
	}
	if short.breaker.auth != time.Minute || short.breaker.rate != time.Second ||
		short.breaker.server != 2*time.Second {
		t.Error("自定义熔断窗口未生效")
	}
}

// TestSortCandidates 覆盖 route.priority → channel.priority → last_used_at →
// route.id 的完整比较链。
func TestSortCandidates(t *testing.T) {
	base := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	earlier := base
	later := base.Add(time.Second)

	candidate := func(routeID int64, routePriority, channelPriority int, lastUsed *time.Time) Candidate {
		return Candidate{
			Route: store.ModelRoute{ID: routeID, Priority: routePriority},
			Channel: store.Channel{
				ID:         routeID,
				Priority:   channelPriority,
				LastUsedAt: lastUsed,
			},
		}
	}
	candidates := []Candidate{
		candidate(1, 2, 0, nil), // route 优先级最高，排最后
		candidate(2, 1, 5, nil),
		candidate(3, 1, 3, &later),
		candidate(4, 1, 3, nil),
		candidate(5, 1, 3, &earlier),
		candidate(6, 1, 3, nil),
	}
	sortCandidates(candidates)
	assertCandidateRouteIDs(t, candidates, 4, 6, 5, 3, 2, 1)
}

// TestSchedulerCandidatesRealStore 覆盖真实仓储的排序、过滤与
// TouchLastUsed 后的顺序变化。
func TestSchedulerCandidatesRealStore(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()

	high := createRoutingChannel(t, channels, "high", store.ChannelProtocolOpenAIResponses, 0, 2, true)
	low := createRoutingChannel(t, channels, "low", store.ChannelProtocolOpenAIResponses, 0, 2, true)
	routePrio := createRoutingChannel(t, channels, "route-prio", store.ChannelProtocolOpenAIResponses, 0, 2, true)

	routeHigh := createRoutingRoute(t, routes, "m", high.ID, 1, true)
	routeLow := createRoutingRoute(t, routes, "m", low.ID, 1, true)
	routePrio5 := createRoutingRoute(t, routes, "m", routePrio.ID, 5, true)

	scheduler := newStoreScheduler(t, s, Options{})

	candidates, err := scheduler.Candidates(ctx, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	// route priority 1 的两条在前（channel priority 相同、均未使用，
	// 按 route.id），route priority 5 的最后。
	assertCandidateRouteIDs(t, candidates, routeHigh.ID, routeLow.ID, routePrio5.ID)

	// 把 high 标记为早已使用：同 route priority、同 channel priority 下，
	// 从未使用的 low 排到 high 前面。
	past := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	if err := channels.TouchLastUsed(ctx, high.ID, past); err != nil {
		t.Fatalf("TouchLastUsed(high): %v", err)
	}
	candidates, err = scheduler.Candidates(ctx, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("Candidates after touch: %v", err)
	}
	assertCandidateRouteIDs(t, candidates, routeLow.ID, routeHigh.ID, routePrio5.ID)

	// Acquire 选择 low 并更新其 last_used_at；释放槽位后顺序再次变化。
	selected, slot, err := scheduler.Acquire(ctx, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if selected.Route.ID != routeLow.ID || selected.Channel.ID != low.ID {
		t.Fatalf("Acquire 选中 route=%d channel=%d，期望 route=%d channel=%d",
			selected.Route.ID, selected.Channel.ID, routeLow.ID, low.ID)
	}
	slot.Release()

	updated, err := channels.Get(ctx, low.ID)
	if err != nil {
		t.Fatalf("Get(low): %v", err)
	}
	if updated.LastUsedAt == nil {
		t.Fatal("Acquire 后 low.last_used_at 仍为 NULL")
	}
	candidates, err = scheduler.Candidates(ctx, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("Candidates after acquire: %v", err)
	}
	assertCandidateRouteIDs(t, candidates, routeHigh.ID, routeLow.ID, routePrio5.ID)
	if got := scheduler.limiter.InUse(low.ID); got != 0 {
		t.Fatalf("low InUse = %d，期望 0", got)
	}
}

// TestSchedulerFiltersCandidates 覆盖禁用映射、禁用渠道、协议不匹配、
// 渠道已删除与熔断打开。
func TestSchedulerFiltersCandidates(t *testing.T) {
	responseProtocol := store.ChannelProtocolOpenAIResponses
	routes := &fakeRouteCatalog{routes: []store.ModelRoute{
		{ID: 1, ClientModel: "m", ChannelID: 1, Priority: 0, Enabled: true},
		{ID: 2, ClientModel: "m", ChannelID: 2, Priority: 0, Enabled: true},
		{ID: 3, ClientModel: "m", ChannelID: 3, Priority: 0, Enabled: true},
		{ID: 4, ClientModel: "m", ChannelID: 1, Priority: 0, Enabled: false},
		{ID: 5, ClientModel: "m", ChannelID: 404, Priority: 0, Enabled: true},
		{ID: 6, ClientModel: "m", ChannelID: 4, Priority: 0, Enabled: true},
	}}
	channels := &fakeChannelCatalog{channels: map[int64]store.Channel{
		1: {ID: 1, Protocol: responseProtocol, Enabled: true, Priority: 0, MaxConcurrency: 1},
		2: {ID: 2, Protocol: responseProtocol, Enabled: false, Priority: 0, MaxConcurrency: 1},
		3: {ID: 3, Protocol: store.ChannelProtocolOpenAIChat, Enabled: true, Priority: 0, MaxConcurrency: 1},
		4: {ID: 4, Protocol: responseProtocol, Enabled: true, Priority: 0, MaxConcurrency: 1},
	}}
	scheduler, err := New(Options{Routes: routes, Channels: channels})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	candidates, err := scheduler.Candidates(context.Background(), "  m  ", "  "+responseProtocol+"  ")
	if err != nil {
		t.Fatalf("Candidates: %v", err)
	}
	assertCandidateRouteIDs(t, candidates, 1, 6)
	if routes.model != "m" || routes.protocol != responseProtocol {
		t.Errorf("仓储收到 model=%q protocol=%q，期望去除空白后的 m/%s",
			routes.model, routes.protocol, responseProtocol)
	}

	// 熔断打开后候选被过滤；成功重置后恢复。
	if !scheduler.RecordFailure(4, 429, "upstream rate limited") {
		t.Fatal("429 应触发熔断")
	}
	candidates, err = scheduler.Candidates(context.Background(), "m", responseProtocol)
	if err != nil {
		t.Fatalf("Candidates with open circuit: %v", err)
	}
	assertCandidateRouteIDs(t, candidates, 1)

	scheduler.RecordSuccess(4)
	candidates, err = scheduler.Candidates(context.Background(), "m", responseProtocol)
	if err != nil {
		t.Fatalf("Candidates after reset: %v", err)
	}
	assertCandidateRouteIDs(t, candidates, 1, 6)

	// 渠道读取失败必须向上返回，而不是静默跳过。
	channels.errors = map[int64]error{4: errors.New("db unavailable")}
	if _, err := scheduler.Candidates(context.Background(), "m", responseProtocol); err == nil {
		t.Fatal("渠道读取失败未被上报")
	}
}

// TestSchedulerNoCandidates 覆盖空模型、协议不匹配与仓储错误。
func TestSchedulerNoCandidates(t *testing.T) {
	ctx := context.Background()
	responseProtocol := store.ChannelProtocolOpenAIResponses

	scheduler, err := New(Options{
		Routes: &fakeRouteCatalog{},
		Channels: &fakeChannelCatalog{channels: map[int64]store.Channel{
			1: {ID: 1, Protocol: store.ChannelProtocolOpenAIChat, Enabled: true, MaxConcurrency: 1},
		}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, _, err := scheduler.Acquire(ctx, "m", responseProtocol); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("Acquire 错误 = %v，期望 ErrNoCandidates", err)
	}
	candidates, err := scheduler.Candidates(ctx, "", responseProtocol)
	if err != nil {
		t.Fatalf("Candidates(empty model): %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("空模型候选 = %+v，期望空", candidates)
	}

	boom := errors.New("route query failed")
	failing := &fakeRouteCatalog{err: boom}
	scheduler, err = New(Options{
		Routes:   failing,
		Channels: &fakeChannelCatalog{},
	})
	if err != nil {
		t.Fatalf("New(failing): %v", err)
	}
	if _, err := scheduler.Candidates(ctx, "m", responseProtocol); !errors.Is(err, boom) {
		t.Fatalf("Candidates 错误 = %v，期望包装 route 查询错误", err)
	}
}

// TestSchedulerCapacityWaitThenError 覆盖全部满载等待、ErrChannelCapacity
// 与释放后继续。
func TestSchedulerCapacityWaitThenError(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()
	protocol := store.ChannelProtocolOpenAIResponses

	alpha := createRoutingChannel(t, channels, "alpha", protocol, 0, 1, true)
	beta := createRoutingChannel(t, channels, "beta", protocol, 0, 1, true)
	routeAlpha := createRoutingRoute(t, routes, "m", alpha.ID, 0, true)
	routeBeta := createRoutingRoute(t, routes, "m", beta.ID, 0, true)

	scheduler := newStoreScheduler(t, s, Options{
		CapacityWait: 80 * time.Millisecond,
		CapacityPoll: 5 * time.Millisecond,
	})

	first, firstSlot, err := scheduler.Acquire(ctx, "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(first): %v", err)
	}
	if first.Route.ID != routeAlpha.ID {
		t.Fatalf("第一个候选 = %d，期望 %d", first.Route.ID, routeAlpha.ID)
	}
	second, secondSlot, err := scheduler.Acquire(ctx, "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(second): %v", err)
	}
	if second.Route.ID != routeBeta.ID {
		t.Fatalf("第二个候选 = %d，期望 %d", second.Route.ID, routeBeta.ID)
	}

	started := time.Now()
	_, _, err = scheduler.Acquire(ctx, "m", protocol)
	elapsed := time.Since(started)
	if !errors.Is(err, ErrChannelCapacity) {
		t.Fatalf("满载错误 = %v，期望 ErrChannelCapacity", err)
	}
	if elapsed < 50*time.Millisecond {
		t.Fatalf("满载等待仅 %v，期望接近 CapacityWait=80ms", elapsed)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("满载等待 %v 过长", elapsed)
	}

	// 释放 beta 后，新的 Acquire 应立刻继续而不是再次等待超时。
	secondSlot.Release()
	third, thirdSlot, err := scheduler.Acquire(ctx, "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(after release): %v", err)
	}
	if third.Channel.ID != beta.ID {
		t.Fatalf("释放后选中渠道 = %d，期望 %d", third.Channel.ID, beta.ID)
	}
	thirdSlot.Release()
	firstSlot.Release()

	if got := scheduler.limiter.InUse(alpha.ID); got != 0 {
		t.Fatalf("alpha InUse = %d，期望 0", got)
	}
	if got := scheduler.limiter.InUse(beta.ID); got != 0 {
		t.Fatalf("beta InUse = %d，期望 0", got)
	}
}

// TestSchedulerWaitsForSlotRelease 覆盖等待期间槽位释放后请求继续。
func TestSchedulerWaitsForSlotRelease(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()
	protocol := store.ChannelProtocolOpenAIResponses

	channel := createRoutingChannel(t, channels, "wait", protocol, 0, 1, true)
	createRoutingRoute(t, routes, "m", channel.ID, 0, true)

	scheduler := newStoreScheduler(t, s, Options{
		CapacityWait: time.Second,
		CapacityPoll: 5 * time.Millisecond,
	})

	first, firstSlot, err := scheduler.Acquire(ctx, "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(first): %v", err)
	}
	if first.Channel.ID != channel.ID {
		t.Fatalf("首个渠道 = %d，期望 %d", first.Channel.ID, channel.ID)
	}

	go func() {
		time.Sleep(40 * time.Millisecond)
		firstSlot.Release()
	}()

	started := time.Now()
	candidate, slot, err := scheduler.Acquire(ctx, "m", protocol)
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("等待槽位释放失败：%v", err)
	}
	if candidate.Channel.ID != channel.ID {
		t.Fatalf("等待后渠道 = %d，期望 %d", candidate.Channel.ID, channel.ID)
	}
	if elapsed < 25*time.Millisecond {
		t.Fatalf("Acquire 在 %v 内返回，未发生等待", elapsed)
	}
	if elapsed > time.Second {
		t.Fatalf("Acquire 等待 %v，超过 CapacityWait", elapsed)
	}
	slot.Release()
	firstSlot.Release()
}

// TestSchedulerAcquireCanceled 覆盖等待期间客户端取消与取消后的槽位复用。
func TestSchedulerAcquireCanceled(t *testing.T) {
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()
	protocol := store.ChannelProtocolOpenAIResponses

	channel := createRoutingChannel(t, channels, "cancel", protocol, 0, 1, true)
	createRoutingRoute(t, routes, "m", channel.ID, 0, true)

	scheduler := newStoreScheduler(t, s, Options{
		CapacityWait: 2 * time.Second,
		CapacityPoll: 5 * time.Millisecond,
	})

	lease, slot, err := scheduler.Acquire(context.Background(), "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(lease): %v", err)
	}
	if lease.Channel.ID != channel.ID {
		t.Fatalf("租约渠道 = %d，期望 %d", lease.Channel.ID, channel.ID)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	started := time.Now()
	_, _, err = scheduler.Acquire(ctx, "m", protocol)
	elapsed := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误 = %v，期望 context.Canceled", err)
	}
	if elapsed > time.Second {
		t.Fatalf("取消后 %v 才返回", elapsed)
	}

	// 模拟流式请求取消后 defer Release：槽位可再次获取。
	slot.Release()
	_, nextSlot, err := scheduler.Acquire(context.Background(), "m", protocol)
	if err != nil {
		t.Fatalf("取消后重新获取槽位失败：%v", err)
	}
	nextSlot.Release()
	if got := scheduler.limiter.InUse(channel.ID); got != 0 {
		t.Fatalf("InUse = %d，期望 0", got)
	}
}

// TestSchedulerConcurrentAcquireHonorsMaxConcurrency 验证并发请求不会超过
// max_concurrency，且所有槽位最终归还。
func TestSchedulerConcurrentAcquireHonorsMaxConcurrency(t *testing.T) {
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()
	protocol := store.ChannelProtocolOpenAIResponses

	channel := createRoutingChannel(t, channels, "concurrent", protocol, 0, 2, true)
	createRoutingRoute(t, routes, "m", channel.ID, 0, true)

	scheduler := newStoreScheduler(t, s, Options{
		CapacityWait: 2 * time.Second,
		CapacityPoll: 2 * time.Millisecond,
	})

	const (
		limit   = 2
		workers = 12
	)
	var active atomic.Int32
	var peak atomic.Int32
	var failures atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, slot, err := scheduler.Acquire(context.Background(), "m", protocol)
			if err != nil {
				failures.Add(1)
				return
			}
			current := active.Add(1)
			for {
				previous := peak.Load()
				if current <= previous || peak.CompareAndSwap(previous, current) {
					break
				}
			}
			time.Sleep(2 * time.Millisecond)
			active.Add(-1)
			slot.Release()
		}()
	}
	wg.Wait()

	if got := failures.Load(); got != 0 {
		t.Fatalf("%d 个并发请求未获得槽位", got)
	}
	if got := peak.Load(); got > limit {
		t.Fatalf("并发峰值 = %d，超过 max_concurrency=%d", got, limit)
	}
	if got := scheduler.limiter.InUse(channel.ID); got != 0 {
		t.Fatalf("全部请求结束后 InUse = %d，期望 0（槽位泄漏）", got)
	}
}

// TestSchedulerAcquireExcluding 覆盖失败切换使用的渠道排除集合。
func TestSchedulerAcquireExcluding(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	channels := s.Channels()
	routes := s.ModelRoutes()
	protocol := store.ChannelProtocolOpenAIResponses

	alpha := createRoutingChannel(t, channels, "exclude-alpha", protocol, 0, 2, true)
	beta := createRoutingChannel(t, channels, "exclude-beta", protocol, 0, 2, true)
	createRoutingRoute(t, routes, "m", alpha.ID, 0, true)
	createRoutingRoute(t, routes, "m", beta.ID, 0, true)

	scheduler := newStoreScheduler(t, s, Options{})

	excluded := map[int64]struct{}{alpha.ID: {}}
	selected, slot, err := scheduler.AcquireExcluding(ctx, "m", protocol, excluded)
	if err != nil {
		t.Fatalf("AcquireExcluding: %v", err)
	}
	if selected.Channel.ID != beta.ID {
		t.Fatalf("排除 alpha 后选中渠道 = %d，期望 %d", selected.Channel.ID, beta.ID)
	}
	slot.Release()

	all := map[int64]struct{}{alpha.ID: {}, beta.ID: {}}
	if _, _, err := scheduler.AcquireExcluding(ctx, "m", protocol, all); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("全部排除错误 = %v，期望 ErrNoCandidates", err)
	}

	// 空排除集合等价于 Acquire；beta 已被使用，应优先选择仍为 NULL 的 alpha。
	selected, slot, err = scheduler.AcquireExcluding(ctx, "m", protocol, map[int64]struct{}{})
	if err != nil {
		t.Fatalf("AcquireExcluding(empty): %v", err)
	}
	if selected.Channel.ID != alpha.ID {
		t.Fatalf("空排除集合选中渠道 = %d，期望 %d", selected.Channel.ID, alpha.ID)
	}
	slot.Release()
	if got := scheduler.limiter.InUse(alpha.ID); got != 0 {
		t.Fatalf("alpha InUse = %d，期望 0", got)
	}
	if got := scheduler.limiter.InUse(beta.ID); got != 0 {
		t.Fatalf("beta InUse = %d，期望 0", got)
	}
}

// TestSchedulerTouchOnlyOnAcquire 验证只有成功获得槽位才写 last_used_at，
// 等待/失败路径不重复写入。
func TestSchedulerTouchOnlyOnAcquire(t *testing.T) {
	fixed := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: fixed}
	protocol := store.ChannelProtocolOpenAIResponses
	recorder := &fakeLastUsed{}
	routes := &fakeRouteCatalog{routes: []store.ModelRoute{
		{ID: 1, ClientModel: "m", ChannelID: 1, Priority: 0, Enabled: true},
	}}
	channels := &fakeChannelCatalog{channels: map[int64]store.Channel{
		1: {ID: 1, Protocol: protocol, Enabled: true, Priority: 0, MaxConcurrency: 1},
	}}
	scheduler, err := New(Options{
		Routes:       routes,
		Channels:     channels,
		LastUsed:     recorder,
		Clock:        clock.Now,
		CapacityWait: 10 * time.Millisecond,
		CapacityPoll: time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, slot, err := scheduler.Acquire(context.Background(), "m", protocol)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	touches := recorder.snapshot()
	if len(touches) != 1 || touches[0].id != 1 || !touches[0].at.Equal(fixed) {
		t.Fatalf("首次获取后的 TouchLastUsed = %+v", touches)
	}

	if _, _, err := scheduler.Acquire(context.Background(), "m", protocol); !errors.Is(err, ErrChannelCapacity) {
		t.Fatalf("满载错误 = %v，期望 ErrChannelCapacity", err)
	}
	if got := len(recorder.snapshot()); got != 1 {
		t.Fatalf("满载等待期间 TouchLastUsed 次数 = %d，期望仍为 1", got)
	}

	slot.Release()
	_, slot, err = scheduler.Acquire(context.Background(), "m", protocol)
	if err != nil {
		t.Fatalf("Acquire(after release): %v", err)
	}
	if got := len(recorder.snapshot()); got != 2 {
		t.Fatalf("再次获取后 TouchLastUsed 次数 = %d，期望 2", got)
	}
	slot.Release()
}

// TestSchedulerCircuitWindows 覆盖 401/403/429/5xx 的熔断窗口、过期恢复、
// 非熔断状态码与成功重置。
func TestSchedulerCircuitWindows(t *testing.T) {
	start := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	clock := &fakeClock{now: start}
	protocol := store.ChannelProtocolOpenAIResponses
	routes := &fakeRouteCatalog{routes: []store.ModelRoute{
		{ID: 1, ClientModel: "m", ChannelID: 1, Priority: 0, Enabled: true},
	}}
	channels := &fakeChannelCatalog{channels: map[int64]store.Channel{
		1: {ID: 1, Protocol: protocol, Enabled: true, Priority: 0, MaxConcurrency: 1},
	}}
	scheduler, err := New(Options{
		Routes:   routes,
		Channels: channels,
		Clock:    clock.Now,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		status int
		window time.Duration
		trips  bool
	}{
		{400, 0, false},
		{401, 5 * time.Minute, true},
		{403, 5 * time.Minute, true},
		{429, 10 * time.Second, true},
		{500, 20 * time.Second, true},
		{503, 20 * time.Second, true},
		{529, 20 * time.Second, true},
		{600, 0, false},
	}
	for _, tt := range tests {
		scheduler.RecordSuccess(1)
		tripped := scheduler.RecordFailure(1, tt.status, "upstream error")
		if tripped != tt.trips {
			t.Errorf("status=%d RecordFailure = %v，期望 %v", tt.status, tripped, tt.trips)
			continue
		}
		if !tt.trips {
			if snapshot := scheduler.Circuit(1); snapshot.Open {
				t.Errorf("status=%d 不应熔断，但 Circuit.Open = true", tt.status)
			}
			continue
		}

		snapshot := scheduler.Circuit(1)
		if !snapshot.Open {
			t.Errorf("status=%d 熔断未打开", tt.status)
		}
		if got := snapshot.OpenUntil.Sub(clock.Now()); got != tt.window {
			t.Errorf("status=%d 窗口 = %v，期望 %v", tt.status, got, tt.window)
		}
		if snapshot.LastError != "upstream error" {
			t.Errorf("status=%d last_error = %q", tt.status, snapshot.LastError)
		}

		candidates, err := scheduler.Candidates(ctx, "m", protocol)
		if err != nil {
			t.Fatalf("Candidates(open): %v", err)
		}
		assertCandidateRouteIDs(t, candidates)
		if _, _, err := scheduler.Acquire(ctx, "m", protocol); !errors.Is(err, ErrNoCandidates) {
			t.Errorf("熔断时 Acquire 错误 = %v，期望 ErrNoCandidates", err)
		}

		clock.Advance(tt.window - time.Millisecond)
		if snapshot := scheduler.Circuit(1); !snapshot.Open {
			t.Errorf("status=%d 窗口未到期就关闭", tt.status)
		}
		clock.Advance(time.Millisecond)
		candidates, err = scheduler.Candidates(ctx, "m", protocol)
		if err != nil {
			t.Fatalf("Candidates(expired): %v", err)
		}
		assertCandidateRouteIDs(t, candidates, 1)
	}

	// 成功重置清空熔断与 last_error。
	scheduler.RecordFailure(1, 503, "server error")
	scheduler.RecordSuccess(1)
	if snapshot := scheduler.Circuit(1); snapshot.Open || snapshot.LastError != "" {
		t.Fatalf("RecordSuccess 后状态 = %+v，期望清空", snapshot)
	}
	candidates, err := scheduler.Candidates(ctx, "m", protocol)
	if err != nil {
		t.Fatalf("Candidates(after success): %v", err)
	}
	assertCandidateRouteIDs(t, candidates, 1)
}

// fakeClock 是可手动推进的测试时钟。
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeRouteCatalog 返回固定映射，便于测试不依赖 SQLite 的过滤逻辑。
type fakeRouteCatalog struct {
	routes   []store.ModelRoute
	err      error
	model    string
	protocol string
}

func (f *fakeRouteCatalog) ListByModelAndProtocol(
	_ context.Context,
	clientModel, protocol string,
) ([]store.ModelRoute, error) {
	f.model = clientModel
	f.protocol = protocol
	if f.err != nil {
		return nil, f.err
	}
	return append([]store.ModelRoute(nil), f.routes...), nil
}

type fakeChannelCatalog struct {
	channels map[int64]store.Channel
	errors   map[int64]error
}

func (f *fakeChannelCatalog) Get(_ context.Context, id int64) (store.Channel, error) {
	if err, ok := f.errors[id]; ok {
		return store.Channel{}, err
	}
	channel, ok := f.channels[id]
	if !ok {
		return store.Channel{}, store.ErrChannelNotFound
	}
	return channel, nil
}

type lastUsedTouch struct {
	id int64
	at time.Time
}

type fakeLastUsed struct {
	mu      sync.Mutex
	touches []lastUsedTouch
}

func (f *fakeLastUsed) TouchLastUsed(_ context.Context, id int64, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches = append(f.touches, lastUsedTouch{id: id, at: at})
	return nil
}

func (f *fakeLastUsed) snapshot() []lastUsedTouch {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]lastUsedTouch(nil), f.touches...)
}
