package store

import (
	"context"
	"errors"
	"testing"

	"ai-router/internal/secret"
)

func TestModelRouteCapabilitiesRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x71)
	channel := createRouteTestChannel(t, s.Channels(), envelope,
		"vision", ChannelProtocolOpenAIResponses, 0, true)

	created, err := s.ModelRoutes().Create(ctx, ModelRouteInput{
		ClientModel:     "gpt-vision",
		ChannelID:       channel.ID,
		UpstreamModel:   "up-vision",
		Enabled:         true,
		InputModalities: ModelModalitiesTextImage,
		ContextWindow:   32000,
		ReasoningLevels: []string{"high", "low", "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.InputModalities != ModelModalitiesTextImage || created.ContextWindow == nil || *created.ContextWindow != 32000 {
		t.Fatalf("created = %+v", created)
	}
	if len(created.ReasoningLevels) != 2 || created.ReasoningLevels[0] != "low" || created.ReasoningLevels[1] != "high" {
		t.Fatalf("levels = %v", created.ReasoningLevels)
	}

	got, err := s.ModelRoutes().Get(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InputModalities != ModelModalitiesTextImage || got.ContextWindow == nil || *got.ContextWindow != 32000 ||
		len(got.ReasoningLevels) != 2 {
		t.Fatalf("get = %+v", got)
	}

	candidates, err := s.ModelRoutes().ListByModelAndProtocol(ctx, "gpt-vision", ChannelProtocolOpenAIResponses)
	if err != nil || len(candidates) != 1 || candidates[0].InputModalities != ModelModalitiesTextImage {
		t.Fatalf("candidates = %+v err=%v", candidates, err)
	}
}

func TestModelRouteCapabilitiesClearSemantics(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x72)
	channel := createRouteTestChannel(t, s.Channels(), envelope,
		"clear", ChannelProtocolOpenAIResponses, 0, true)

	created, err := s.ModelRoutes().Create(ctx, ModelRouteInput{
		ClientModel: "m", ChannelID: channel.ID, UpstreamModel: "u", Enabled: true,
		InputModalities: ModelModalitiesText, ContextWindow: 8000, ReasoningLevels: []string{"low"},
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.ModelRoutes().Update(ctx, created.ID, ModelRouteInput{
		ClientModel: "m", ChannelID: channel.ID, UpstreamModel: "u", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.InputModalities != ModelModalitiesUnknown || updated.ContextWindow != nil || len(updated.ReasoningLevels) != 0 {
		t.Fatalf("cleared = %+v", updated)
	}
}

func TestModelRouteCapabilitiesValidation(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x73)
	channel := createRouteTestChannel(t, s.Channels(), envelope,
		"invalid", ChannelProtocolOpenAIResponses, 0, true)

	cases := []ModelRouteInput{
		{InputModalities: "audio"},
		{ContextWindow: -1},
		{ContextWindow: ModelContextWindowMax + 1},
		{ReasoningLevels: []string{"turbo"}},
		{ReasoningLevels: []string{"low", " "}},
	}
	for i, in := range cases {
		in.ClientModel, in.ChannelID, in.UpstreamModel, in.Enabled = "m", channel.ID, "u", true
		if _, err := s.ModelRoutes().Create(ctx, in); !errors.Is(err, ErrInvalidModelRoute) {
			t.Fatalf("case %d: err = %v", i, err)
		}
	}
	if got := modelRouteRowCount(t, s); got != 0 {
		t.Fatalf("invalid inputs wrote %d rows", got)
	}

	// 绕过 Go 校验，SQLite CHECK 也必须拒绝非法模态与越界窗口。
	created, err := s.ModelRoutes().Create(ctx, ModelRouteInput{
		ClientModel: "m", ChannelID: channel.ID, UpstreamModel: "u", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().Exec(`UPDATE model_routes SET input_modalities = 'audio' WHERE id = ?`, created.ID); err == nil {
		t.Fatal("SQLite CHECK accepted invalid modality")
	}
	if _, err := s.DB().Exec(`UPDATE model_routes SET context_window = 0 WHERE id = ?`, created.ID); err == nil {
		t.Fatal("SQLite CHECK accepted zero window")
	}
}

func TestAggregateRouteCapabilities(t *testing.T) {
	window := func(v int64) *int64 { return &v }
	cases := []struct {
		name   string
		routes []ModelRoute
		want   EffectiveRouteCapabilities
	}{
		{name: "empty", routes: nil, want: EffectiveRouteCapabilities{RouteCount: 0, InputModalities: ModelModalitiesUnknown, ReasoningLevels: []string{}}},
		{name: "single", routes: []ModelRoute{{InputModalities: ModelModalitiesTextImage, ContextWindow: window(32000), ReasoningLevels: []string{"low", "high"}}},
			want: EffectiveRouteCapabilities{RouteCount: 1, InputModalities: ModelModalitiesTextImage, ContextWindow: window(32000), ReasoningLevels: []string{"low", "high"}}},
		{name: "min window", routes: []ModelRoute{
			{InputModalities: ModelModalitiesTextImage, ContextWindow: window(32000)},
			{InputModalities: ModelModalitiesTextImage, ContextWindow: window(200000)},
		}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesTextImage, ContextWindow: window(32000), ReasoningLevels: []string{}}},
		{name: "text narrows", routes: []ModelRoute{
			{InputModalities: ModelModalitiesTextImage, ContextWindow: window(32000)},
			{InputModalities: ModelModalitiesText, ContextWindow: window(64000)},
		}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesText, ContextWindow: window(32000), ReasoningLevels: []string{}}},
		{name: "undeclared window", routes: []ModelRoute{
			{InputModalities: ModelModalitiesTextImage, ContextWindow: window(200000)},
			{InputModalities: ModelModalitiesTextImage},
		}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesTextImage, ContextWindow: window(200000), HasUndeclaredWindow: true, ReasoningLevels: []string{}}},
		{name: "all unknown", routes: []ModelRoute{{}, {}}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesUnknown, HasUndeclaredWindow: true, ReasoningLevels: []string{}}},
		{name: "reasoning intersection", routes: []ModelRoute{
			{InputModalities: ModelModalitiesTextImage, ReasoningLevels: []string{"none", "low", "high"}},
			{InputModalities: ModelModalitiesTextImage, ReasoningLevels: []string{"low", "medium", "high"}},
		}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesTextImage, HasUndeclaredWindow: true, ReasoningLevels: []string{"low", "high"}}},
		{name: "reasoning undeclared", routes: []ModelRoute{
			{InputModalities: ModelModalitiesTextImage, ReasoningLevels: []string{"low"}},
			{InputModalities: ModelModalitiesTextImage},
		}, want: EffectiveRouteCapabilities{RouteCount: 2, InputModalities: ModelModalitiesTextImage, HasUndeclaredWindow: true, ReasoningLevels: []string{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := AggregateRouteCapabilities(tc.routes)
			if got.RouteCount != tc.want.RouteCount || got.InputModalities != tc.want.InputModalities ||
				got.HasUndeclaredWindow != tc.want.HasUndeclaredWindow || len(got.ReasoningLevels) != len(tc.want.ReasoningLevels) {
				t.Fatalf("got %+v want %+v", got, tc.want)
			}
			if (got.ContextWindow == nil) != (tc.want.ContextWindow == nil) ||
				(got.ContextWindow != nil && *got.ContextWindow != *tc.want.ContextWindow) {
				t.Fatalf("window got %v want %v", got.ContextWindow, tc.want.ContextWindow)
			}
			for i := range got.ReasoningLevels {
				if got.ReasoningLevels[i] != tc.want.ReasoningLevels[i] {
					t.Fatalf("levels got %v want %v", got.ReasoningLevels, tc.want.ReasoningLevels)
				}
			}
		})
	}
}

func TestModelRoutesListByProtocol(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x74)
	responses := createRouteTestChannel(t, s.Channels(), envelope,
		"resp", ChannelProtocolOpenAIResponses, 5, true)
	chat := createRouteTestChannel(t, s.Channels(), envelope,
		"chat", ChannelProtocolOpenAIChat, 1, true)
	disabled := createRouteTestChannel(t, s.Channels(), envelope,
		"off", ChannelProtocolOpenAIResponses, 0, false)

	for _, spec := range []struct {
		model   string
		channel int64
		enabled bool
	}{
		{"b", responses.ID, true},
		{"a", responses.ID, true},
		{"a", disabled.ID, true},
		{"c", chat.ID, true},
		{"d", responses.ID, false},
	} {
		if _, err := s.ModelRoutes().Create(ctx, ModelRouteInput{
			ClientModel: spec.model, ChannelID: spec.channel, UpstreamModel: spec.model + "-up", Enabled: spec.enabled,
		}); err != nil {
			t.Fatal(err)
		}
	}

	routes, err := s.ModelRoutes().ListByProtocol(ctx, ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 2 || routes[0].ClientModel != "a" || routes[1].ClientModel != "b" {
		t.Fatalf("routes = %+v", routes)
	}
	models, err := s.ModelRoutes().ListModelsByProtocol(ctx, ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 2 || models[0] != "a" || models[1] != "b" {
		t.Fatalf("models = %v", models)
	}
	if empty, err := s.ModelRoutes().ListByProtocol(ctx, " "); err != nil || len(empty) != 0 {
		t.Fatalf("blank protocol = %v %v", empty, err)
	}
}

func TestCreateWithModelsCapabilityDefaults(t *testing.T) {
	ctx := context.Background()
	s := newChannelTestStore(t)
	envelope := testChannelEnvelope(t, 0x75)
	channel, routes, err := s.Channels().CreateWithModels(ctx, newEncryptedChannelInput(t,
		envelope, "auto", "https://upstream.example.com/v1", ChannelProtocolOpenAIResponses,
		"sk-auto-0123456789", 0, 2, true), []string{"m1", "m2"})
	if err != nil || len(routes) != 2 {
		t.Fatalf("CreateWithModels: %v %d", err, len(routes))
	}
	_ = channel

	var modalities string
	var window, levels any
	if err := s.DB().QueryRow(`SELECT input_modalities, context_window, reasoning_levels FROM model_routes ORDER BY id LIMIT 1`).
		Scan(&modalities, &window, &levels); err != nil {
		t.Fatal(err)
	}
	if modalities != ModelModalitiesUnknown || window != nil || levels != nil {
		t.Fatalf("raw defaults = %q %v %v", modalities, window, levels)
	}
	got, err := s.ModelRoutes().Get(ctx, routes[0].ID)
	if err != nil || got.InputModalities != ModelModalitiesUnknown || got.ContextWindow != nil || len(got.ReasoningLevels) != 0 {
		t.Fatalf("route = %+v err=%v", got, err)
	}
}

var _ = secret.PurposeChannelKey
