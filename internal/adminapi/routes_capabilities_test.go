package adminapi

import (
	"net/http"
	"strconv"
	"testing"

	"ai-router/internal/store"
)

func TestModelRouteCapabilitiesAPI(t *testing.T) {
	api, _, cookie, _ := newModelRoutesAPI(t)
	channel := createRouteChannel(t, api, cookie,
		"capability-channel", store.ChannelProtocolOpenAIResponses)

	body := validModelRouteBody("cap-model", channel.ID)
	body["input_modalities"] = store.ModelModalitiesTextImage
	body["context_window"] = 32000
	body["reasoning_levels"] = []string{"high", "low"}
	created := createModelRoute(t, api, cookie, body)
	if created.InputModalities != store.ModelModalitiesTextImage || created.ContextWindow == nil ||
		*created.ContextWindow != 32000 || len(created.ReasoningLevels) != 2 ||
		created.ReasoningLevels[0] != "low" || created.ReasoningLevels[1] != "high" {
		t.Fatalf("created = %+v", created)
	}

	// 省略能力字段只启停，不得清空已声明能力。
	toggle := apiRequest(t, api, http.MethodPatch, "/admin/api/model-routes/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"enabled": false}, testOrigin, cookie)
	if toggle.Code != http.StatusOK {
		t.Fatalf("toggle = %d %s", toggle.Code, toggle.Body.String())
	}
	updated := decodeBody[modelRouteView](t, toggle)
	if updated.InputModalities != store.ModelModalitiesTextImage || updated.ContextWindow == nil ||
		*updated.ContextWindow != 32000 || len(updated.ReasoningLevels) != 2 {
		t.Fatalf("toggle cleared capabilities: %+v", updated)
	}

	// 显式清空：context_window=0，reasoning_levels=[]。
	clear := apiRequest(t, api, http.MethodPatch, "/admin/api/model-routes/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"context_window": 0, "reasoning_levels": []string{}}, testOrigin, cookie)
	if clear.Code != http.StatusOK {
		t.Fatalf("clear = %d %s", clear.Code, clear.Body.String())
	}
	cleared := decodeBody[modelRouteView](t, clear)
	if cleared.ContextWindow != nil || len(cleared.ReasoningLevels) != 0 ||
		cleared.InputModalities != store.ModelModalitiesTextImage {
		t.Fatalf("cleared = %+v", cleared)
	}

	// 非法能力值返回稳定 400，且不落库。
	invalid := apiRequest(t, api, http.MethodPatch, "/admin/api/model-routes/"+strconv.FormatInt(created.ID, 10),
		map[string]any{"input_modalities": "audio"}, testOrigin, cookie)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid = %d %s", invalid.Code, invalid.Body.String())
	}
}
