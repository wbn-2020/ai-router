package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"ai-router/internal/secret"
)

// createRouteTestChannel 通过渠道仓储创建测试渠道，返回落库记录。
func createRouteTestChannel(
	t *testing.T,
	repo *Channels,
	envelope *secret.Envelope,
	name, protocol string,
	priority int,
	enabled bool,
) Channel {
	t.Helper()
	channel, err := repo.Create(context.Background(), newEncryptedChannelInput(t,
		envelope, name, "https://upstream.example.com/v1", protocol,
		"sk-"+name+"-0123456789", priority, 2, enabled))
	if err != nil {
		t.Fatalf("创建渠道 %s: %v", name, err)
	}
	return channel
}

func modelRouteRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var count int
	if err := s.DB().QueryRow(`SELECT count(*) FROM model_routes`).Scan(&count); err != nil {
		t.Fatalf("统计 model_routes: %v", err)
	}
	return count
}

// TestModelRoutesCRUD 覆盖创建、查询、更新、唯一约束与删除。
func TestModelRoutesCRUD(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x61)
	channels := s.Channels()
	routes := s.ModelRoutes()

	primary := createRouteTestChannel(t, channels, envelope,
		"primary", ChannelProtocolOpenAIResponses, 10, true)
	secondary := createRouteTestChannel(t, channels, envelope,
		"secondary", ChannelProtocolOpenAIChat, 5, true)
	tertiary := createRouteTestChannel(t, channels, envelope,
		"tertiary", ChannelProtocolOpenAIResponses, 20, true)

	created, err := routes.Create(ctx, ModelRouteInput{
		ClientModel:   "  gpt-5  ",
		ChannelID:     primary.ID,
		UpstreamModel: "  gpt-5-upstream  ",
		Priority:      7,
		Enabled:       false,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.ID <= 0 {
		t.Fatalf("Create 返回的 ID = %d", created.ID)
	}
	if created.ClientModel != "gpt-5" || created.UpstreamModel != "gpt-5-upstream" {
		t.Errorf("Create 未去除首尾空白：%+v", created)
	}
	if created.ChannelID != primary.ID || created.Priority != 7 || created.Enabled {
		t.Errorf("Create 返回配置不符：%+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("Create 未写入时间戳")
	}

	// 同一 client_model + channel_id 已存在（即使上游模型不同、映射禁用）。
	_, err = routes.Create(ctx, ModelRouteInput{
		ClientModel:   "gpt-5",
		ChannelID:     primary.ID,
		UpstreamModel: "other-upstream",
		Priority:      0,
		Enabled:       true,
	})
	if !errors.Is(err, ErrModelRouteConflict) {
		t.Fatalf("重复映射错误 = %v，期望 ErrModelRouteConflict", err)
	}
	if count := modelRouteRowCount(t, s); count != 1 {
		t.Fatalf("重复创建后 model_routes 行数 = %d，期望 1", count)
	}

	// 同一客户端模型可以映射到不同渠道。
	second, err := routes.Create(ctx, ModelRouteInput{
		ClientModel:   "gpt-5",
		ChannelID:     secondary.ID,
		UpstreamModel: "gpt-5-chat",
		Priority:      0,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("不同渠道创建: %v", err)
	}
	// 同一渠道可以承载不同客户端模型。
	other, err := routes.Create(ctx, ModelRouteInput{
		ClientModel:   "gpt-5-mini",
		ChannelID:     primary.ID,
		UpstreamModel: "gpt-5-mini-upstream",
		Priority:      2,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("不同模型创建: %v", err)
	}

	got, err := routes.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ClientModel != "gpt-5" || got.ChannelID != primary.ID || got.Enabled {
		t.Errorf("Get = %+v", got)
	}
	if _, err := routes.Get(ctx, 99999); !errors.Is(err, ErrModelRouteNotFound) {
		t.Fatalf("Get 不存在错误 = %v，期望 ErrModelRouteNotFound", err)
	}

	list, err := routes.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("List 长度 = %d，期望 3：%+v", len(list), list)
	}
	// 排序：client_model ASC，同模型内 priority ASC。
	if list[0].ID != second.ID || list[1].ID != created.ID || list[2].ID != other.ID {
		t.Errorf("List 顺序 = [%d %d %d]，期望 [%d %d %d]",
			list[0].ID, list[1].ID, list[2].ID, second.ID, created.ID, other.ID)
	}

	// 全量更新：移动渠道、修改上游模型、优先级与启停。
	updated, err := routes.Update(ctx, created.ID, ModelRouteInput{
		ClientModel:   "gpt-5",
		ChannelID:     tertiary.ID,
		UpstreamModel: "gpt-5-v2",
		Priority:      3,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.ChannelID != tertiary.ID || updated.UpstreamModel != "gpt-5-v2" ||
		updated.Priority != 3 || !updated.Enabled {
		t.Errorf("Update 后 = %+v", updated)
	}

	// 更新成已存在的 client_model + channel_id 组合必须冲突，且原记录保持。
	_, err = routes.Update(ctx, created.ID, ModelRouteInput{
		ClientModel:   "gpt-5",
		ChannelID:     secondary.ID,
		UpstreamModel: "conflict",
		Priority:      1,
		Enabled:       true,
	})
	if !errors.Is(err, ErrModelRouteConflict) {
		t.Fatalf("冲突更新错误 = %v，期望 ErrModelRouteConflict", err)
	}
	reloaded, err := routes.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get after conflict: %v", err)
	}
	if reloaded.ChannelID != tertiary.ID || reloaded.UpstreamModel != "gpt-5-v2" {
		t.Errorf("冲突更新改动了原记录：%+v", reloaded)
	}

	if _, err := routes.Update(ctx, 99999, ModelRouteInput{
		ClientModel:   "missing",
		ChannelID:     primary.ID,
		UpstreamModel: "missing",
		Priority:      0,
		Enabled:       true,
	}); !errors.Is(err, ErrModelRouteNotFound) {
		t.Fatalf("更新不存在映射错误 = %v，期望 ErrModelRouteNotFound", err)
	}

	for _, id := range []int64{created.ID, second.ID, other.ID} {
		if err := routes.Delete(ctx, id); err != nil {
			t.Fatalf("Delete(%d): %v", id, err)
		}
	}
	if count := modelRouteRowCount(t, s); count != 0 {
		t.Fatalf("删除后 model_routes 行数 = %d", count)
	}
	if err := routes.Delete(ctx, created.ID); !errors.Is(err, ErrModelRouteNotFound) {
		t.Fatalf("重复删除错误 = %v，期望 ErrModelRouteNotFound", err)
	}
	if _, err := routes.Get(ctx, created.ID); !errors.Is(err, ErrModelRouteNotFound) {
		t.Fatalf("删除后 Get 错误 = %v，期望 ErrModelRouteNotFound", err)
	}

	// 删除渠道由 FK 级联删除映射。
	cascade, err := routes.Create(ctx, ModelRouteInput{
		ClientModel:   "gpt-5",
		ChannelID:     tertiary.ID,
		UpstreamModel: "cascade",
		Priority:      0,
		Enabled:       true,
	})
	if err != nil {
		t.Fatalf("创建级联映射: %v", err)
	}
	if err := channels.Delete(ctx, tertiary.ID); err != nil {
		t.Fatalf("删除渠道: %v", err)
	}
	if _, err := routes.Get(ctx, cascade.ID); !errors.Is(err, ErrModelRouteNotFound) {
		t.Fatalf("渠道删除后映射仍存在：%v", err)
	}
}

// TestModelRoutesValidation 覆盖模型名、渠道、优先级校验；失败输入不得落库。
func TestModelRoutesValidation(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x62)
	channels := s.Channels()
	routes := s.ModelRoutes()
	channel := createRouteTestChannel(t, channels, envelope,
		"valid", ChannelProtocolOpenAIResponses, 0, true)

	valid := func() ModelRouteInput {
		return ModelRouteInput{
			ClientModel:   "gpt-5",
			ChannelID:     channel.ID,
			UpstreamModel: "gpt-5-upstream",
			Priority:      0,
			Enabled:       true,
		}
	}

	tests := []struct {
		name   string
		mutate func(*ModelRouteInput)
		want   error
	}{
		{"空 client_model", func(in *ModelRouteInput) { in.ClientModel = "   " }, ErrInvalidModelRoute},
		{"client_model 含控制字符", func(in *ModelRouteInput) { in.ClientModel = "bad\nmodel" }, ErrInvalidModelRoute},
		{"空 upstream_model", func(in *ModelRouteInput) { in.UpstreamModel = "   " }, ErrInvalidModelRoute},
		{"upstream_model 含控制字符", func(in *ModelRouteInput) { in.UpstreamModel = "bad\x00model" }, ErrInvalidModelRoute},
		{"channel_id 为 0", func(in *ModelRouteInput) { in.ChannelID = 0 }, ErrInvalidModelRoute},
		{"channel_id 为负", func(in *ModelRouteInput) { in.ChannelID = -3 }, ErrInvalidModelRoute},
		{"channel_id 不存在", func(in *ModelRouteInput) { in.ChannelID = 99999 }, ErrModelRouteChannelNotFound},
		{"priority 过小", func(in *ModelRouteInput) { in.Priority = -1 }, ErrInvalidModelRoute},
		{"priority 过大", func(in *ModelRouteInput) { in.Priority = 1001 }, ErrInvalidModelRoute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid()
			tt.mutate(&input)
			if _, err := routes.Create(ctx, input); !errors.Is(err, tt.want) {
				t.Fatalf("Create 错误 = %v，期望 %v", err, tt.want)
			}
			if _, err := routes.Create(ctx, input); !errors.Is(err, tt.want) {
				t.Fatalf("重复 Create 错误 = %v，期望 %v", err, tt.want)
			}
		})
	}
	if count := modelRouteRowCount(t, s); count != 0 {
		t.Fatalf("非法输入落库 %d 行", count)
	}
}

// TestModelRoutesListByModelAndProtocol 覆盖启用过滤、协议匹配与稳定排序。
func TestModelRoutesListByModelAndProtocol(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x63)
	channels := s.Channels()
	routes := s.ModelRoutes()

	respA := createRouteTestChannel(t, channels, envelope,
		"resp-a", ChannelProtocolOpenAIResponses, 20, true)
	respB := createRouteTestChannel(t, channels, envelope,
		"resp-b", ChannelProtocolOpenAIResponses, 10, true)
	respC := createRouteTestChannel(t, channels, envelope,
		"resp-c", ChannelProtocolOpenAIResponses, 10, true)
	respFast := createRouteTestChannel(t, channels, envelope,
		"resp-fast", ChannelProtocolOpenAIResponses, 5, true)
	chat := createRouteTestChannel(t, channels, envelope,
		"chat", ChannelProtocolOpenAIChat, 0, true)
	disabledChannel := createRouteTestChannel(t, channels, envelope,
		"resp-disabled", ChannelProtocolOpenAIResponses, 0, false)
	respUnused := createRouteTestChannel(t, channels, envelope,
		"resp-unused", ChannelProtocolOpenAIResponses, 0, true)

	create := func(model string, channelID int64, upstream string, priority int, enabled bool) ModelRoute {
		t.Helper()
		route, err := routes.Create(ctx, ModelRouteInput{
			ClientModel:   model,
			ChannelID:     channelID,
			UpstreamModel: upstream,
			Priority:      priority,
			Enabled:       enabled,
		})
		if err != nil {
			t.Fatalf("创建映射 %s/%d: %v", model, channelID, err)
		}
		return route
	}

	routeA := create("m", respA.ID, "up-a", 5, true)
	routeB := create("m", respB.ID, "up-b", 1, true)
	routeC := create("m", respC.ID, "up-c", 1, true)
	routeFast := create("m", respFast.ID, "up-fast", 1, true)
	chatRoute := create("m", chat.ID, "up-chat", 0, true)
	create("m", disabledChannel.ID, "up-disabled-channel", 0, true)
	create("m", respUnused.ID, "up-disabled-route", 0, false)
	create("other", respA.ID, "up-other", 0, true)

	assertIDs := func(label string, got []ModelRoute, want ...int64) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s 长度 = %d，期望 %d：%+v", label, len(got), len(want), got)
		}
		for i := range want {
			if got[i].ID != want[i] {
				t.Errorf("%s[%d] = %d，期望 %d", label, i, got[i].ID, want[i])
			}
		}
	}

	candidates, err := routes.ListByModelAndProtocol(ctx, "m", ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("ListByModelAndProtocol: %v", err)
	}
	// route.priority ASC → channel.priority ASC → id ASC；禁用渠道/禁用映射被过滤。
	assertIDs("responses 候选", candidates, routeFast.ID, routeB.ID, routeC.ID, routeA.ID)

	// 客户端模型首尾空白被忽略。
	trimmed, err := routes.ListByModelAndProtocol(ctx, "  m  ", ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("ListByModelAndProtocol(trim): %v", err)
	}
	assertIDs("trim 候选", trimmed, routeFast.ID, routeB.ID, routeC.ID, routeA.ID)

	// last_used_at 升序（NULL 最先）：respB 使用后应排到 respC 之后。
	if err := channels.TouchLastUsed(ctx, respB.ID, time.Now().UTC()); err != nil {
		t.Fatalf("TouchLastUsed: %v", err)
	}
	candidates, err = routes.ListByModelAndProtocol(ctx, "m", ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("ListByModelAndProtocol after touch: %v", err)
	}
	assertIDs("last_used 候选", candidates, routeFast.ID, routeC.ID, routeB.ID, routeA.ID)

	// 按真实时间而不是 RFC3339Nano 字符串排序：整秒时间戳必须早于同秒内
	// 带小数秒的时间戳（字符串比较会把 "Z" 排在 "." 之后而得到相反结果）。
	base := time.Unix(1_800_000_000, 0).UTC()
	if err := channels.TouchLastUsed(ctx, respB.ID, base); err != nil {
		t.Fatalf("TouchLastUsed(respB): %v", err)
	}
	if err := channels.TouchLastUsed(ctx, respC.ID, base.Add(100*time.Millisecond)); err != nil {
		t.Fatalf("TouchLastUsed(respC): %v", err)
	}
	candidates, err = routes.ListByModelAndProtocol(ctx, "m", ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("ListByModelAndProtocol after precise touch: %v", err)
	}
	assertIDs("时间戳精度候选", candidates, routeFast.ID, routeB.ID, routeC.ID, routeA.ID)

	// 协议必须匹配：Chat 候选只含 Chat 渠道。
	chatCandidates, err := routes.ListByModelAndProtocol(ctx, "m", ChannelProtocolOpenAIChat)
	if err != nil {
		t.Fatalf("Chat 候选: %v", err)
	}
	assertIDs("chat 候选", chatCandidates, chatRoute.ID)

	// 其他模型不影响当前模型。
	otherCandidates, err := routes.ListByModelAndProtocol(ctx, "other", ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("other 候选: %v", err)
	}
	if len(otherCandidates) != 1 || otherCandidates[0].ClientModel != "other" {
		t.Fatalf("other 候选 = %+v", otherCandidates)
	}

	// 未配置的协议/模型/空白输入返回空集合。
	for _, tt := range []struct {
		model    string
		protocol string
	}{
		{"m", ChannelProtocolAnthropicMessages},
		{"unknown", ChannelProtocolOpenAIResponses},
		{"", ChannelProtocolOpenAIResponses},
		{"m", ""},
	} {
		got, err := routes.ListByModelAndProtocol(ctx, tt.model, tt.protocol)
		if err != nil {
			t.Fatalf("ListByModelAndProtocol(%q, %q): %v", tt.model, tt.protocol, err)
		}
		if got == nil {
			t.Errorf("ListByModelAndProtocol(%q, %q) 返回 nil，期望空集合", tt.model, tt.protocol)
		}
		if len(got) != 0 {
			t.Errorf("ListByModelAndProtocol(%q, %q) = %+v，期望空", tt.model, tt.protocol, got)
		}
	}
}

// TestModelRoutesListModels 覆盖启用映射去重、禁用/删除后模型消失与渠道级联。
func TestModelRoutesListModels(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x64)
	channels := s.Channels()
	routes := s.ModelRoutes()

	channelA := createRouteTestChannel(t, channels, envelope,
		"models-a", ChannelProtocolOpenAIResponses, 0, true)
	channelB := createRouteTestChannel(t, channels, envelope,
		"models-b", ChannelProtocolOpenAIChat, 0, true)

	create := func(model string, channelID int64, enabled bool) ModelRoute {
		t.Helper()
		route, err := routes.Create(ctx, ModelRouteInput{
			ClientModel:   model,
			ChannelID:     channelID,
			UpstreamModel: model + "-upstream",
			Priority:      0,
			Enabled:       enabled,
		})
		if err != nil {
			t.Fatalf("创建映射 %s: %v", model, err)
		}
		return route
	}

	alphaA := create("alpha", channelA.ID, true)
	create("alpha", channelB.ID, true)
	beta := create("beta", channelB.ID, true)
	create("gamma", channelA.ID, false)

	models, err := routes.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0] != "alpha" || models[1] != "beta" {
		t.Fatalf("ListModels = %v，期望 [alpha beta]", models)
	}

	// 按入站协议过滤：alpha 同时有 Responses/Chat 渠道，beta 只有 Chat。
	responsesModels, err := routes.ListModelsByProtocol(ctx, ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatalf("ListModelsByProtocol(responses): %v", err)
	}
	if len(responsesModels) != 1 || responsesModels[0] != "alpha" {
		t.Fatalf("Responses 模型 = %v，期望 [alpha]", responsesModels)
	}
	chatModels, err := routes.ListModelsByProtocol(ctx, ChannelProtocolOpenAIChat)
	if err != nil {
		t.Fatalf("ListModelsByProtocol(chat): %v", err)
	}
	if len(chatModels) != 2 || chatModels[0] != "alpha" || chatModels[1] != "beta" {
		t.Fatalf("Chat 模型 = %v，期望 [alpha beta]", chatModels)
	}

	// 禁用 alpha 的一条映射，alpha 仍由另一条启用映射支撑。
	if _, err := routes.Update(ctx, alphaA.ID, ModelRouteInput{
		ClientModel:   "alpha",
		ChannelID:     channelA.ID,
		UpstreamModel: "alpha-upstream",
		Priority:      0,
		Enabled:       false,
	}); err != nil {
		t.Fatalf("禁用 alpha 映射: %v", err)
	}
	models, err = routes.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels after disable: %v", err)
	}
	if len(models) != 2 || models[0] != "alpha" || models[1] != "beta" {
		t.Fatalf("禁用单条映射后 ListModels = %v", models)
	}

	// 删除 beta 的最后一条启用映射后，beta 从模型列表消失。
	if err := routes.Delete(ctx, beta.ID); err != nil {
		t.Fatalf("删除 beta 映射: %v", err)
	}
	models, err = routes.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels after delete: %v", err)
	}
	if len(models) != 1 || models[0] != "alpha" {
		t.Fatalf("删除 beta 后 ListModels = %v", models)
	}

	// 删除渠道级联删除 alpha 的剩余映射，alpha 消失。
	if err := channels.Delete(ctx, channelB.ID); err != nil {
		t.Fatalf("删除 channelB: %v", err)
	}
	models, err = routes.ListModels(ctx)
	if err != nil {
		t.Fatalf("ListModels after cascade: %v", err)
	}
	if models == nil {
		t.Fatal("ListModels 返回 nil，期望空数组")
	}
	if len(models) != 0 {
		t.Fatalf("渠道级联后 ListModels = %v，期望空", models)
	}
}
