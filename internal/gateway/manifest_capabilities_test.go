package gateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ai-router/internal/store"
)

type capabilityCatalog struct {
	routes     []store.ModelRoute
	byProtocol map[string][]store.ModelRoute
}

func (c capabilityCatalog) ListModels(context.Context) ([]string, error) {
	seen := make(map[string]struct{})
	models := make([]string, 0)
	for _, route := range c.routes {
		if _, ok := seen[route.ClientModel]; ok {
			continue
		}
		seen[route.ClientModel] = struct{}{}
		models = append(models, route.ClientModel)
	}
	return models, nil
}

func (c capabilityCatalog) ListByModelAndProtocol(_ context.Context, model, protocol string) ([]store.ModelRoute, error) {
	routes := make([]store.ModelRoute, 0)
	for _, route := range c.byProtocol[protocol] {
		if route.ClientModel == model {
			routes = append(routes, route)
		}
	}
	return routes, nil
}

func (c capabilityCatalog) ListByProtocol(_ context.Context, protocol string) ([]store.ModelRoute, error) {
	return c.byProtocol[protocol], nil
}

type capturedInfo struct {
	info RequestInfo
	hit  bool
}

func (c *capturedInfo) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.info, c.hit = RequestInfoFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"ok": "1"})
}

func newCapabilityHandler(t *testing.T, catalog capabilityCatalog, forwarder http.Handler) *Handler {
	t.Helper()
	handler, err := New(Options{
		ClientKey:          testClientKey,
		Routes:             catalog,
		Forwarder:          forwarder,
		CodexContextWindow: 128000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func manifestModels(t *testing.T, handler *Handler) []map[string]json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, PathCodexModels, nil)
	req.Header.Set("Authorization", "Bearer "+testClientKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest = %d %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Models []map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	return payload.Models
}

func TestManifestUsesCommonCandidateCapabilities(t *testing.T) {
	windowA, windowB := int64(200000), int64(32000)
	catalog := capabilityCatalog{
		byProtocol: map[string][]store.ModelRoute{
			store.ChannelProtocolOpenAIResponses: {
				{ClientModel: "alias", ChannelID: 1, Enabled: true,
					InputModalities: store.ModelModalitiesTextImage, ContextWindow: &windowA,
					ReasoningLevels: []string{"low", "high"}},
				{ClientModel: "alias", ChannelID: 2, Enabled: true,
					InputModalities: store.ModelModalitiesText, ContextWindow: &windowB,
					ReasoningLevels: []string{"low", "medium", "high"}},
			},
		},
	}
	handler := newCapabilityHandler(t, catalog, &capturedInfo{})
	models := manifestModels(t, handler)
	if len(models) != 1 {
		t.Fatalf("models = %d", len(models))
	}
	var modalities []string
	var contextWindow int64
	var levels []codexReasoningLevel
	if err := json.Unmarshal(models[0]["input_modalities"], &modalities); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(models[0]["context_window"], &contextWindow); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(models[0]["supported_reasoning_levels"], &levels); err != nil {
		t.Fatal(err)
	}
	if len(modalities) != 1 || modalities[0] != "text" {
		t.Fatalf("modalities = %v", modalities)
	}
	if contextWindow != 32000 {
		t.Fatalf("window = %d", contextWindow)
	}
	if len(levels) != 2 || levels[0].Effort != "low" || levels[1].Effort != "high" {
		t.Fatalf("levels = %+v", levels)
	}
}

func TestManifestUndeclaredDefaultsToTextImage(t *testing.T) {
	catalog := capabilityCatalog{
		byProtocol: map[string][]store.ModelRoute{
			store.ChannelProtocolOpenAIResponses: {
				{ClientModel: "plain", ChannelID: 1, Enabled: true, InputModalities: store.ModelModalitiesUnknown},
			},
		},
	}
	models := manifestModels(t, newCapabilityHandler(t, catalog, &capturedInfo{}))
	var modalities []string
	var window int64
	if err := json.Unmarshal(models[0]["input_modalities"], &modalities); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(models[0]["context_window"], &window); err != nil {
		t.Fatal(err)
	}
	if len(modalities) != 2 || modalities[0] != "text" || modalities[1] != "image" {
		t.Fatalf("modalities = %v", modalities)
	}
	if window != 128000 {
		t.Fatalf("window = %d", window)
	}
}

func TestImageAdmissionRejectsDeclaredTextAlias(t *testing.T) {
	catalog := capabilityCatalog{
		byProtocol: map[string][]store.ModelRoute{
			store.ChannelProtocolOpenAIResponses: {
				{ClientModel: "text-only", ChannelID: 1, Enabled: true, InputModalities: store.ModelModalitiesText},
			},
		},
	}
	forwarder := &capturedInfo{}
	handler := newCapabilityHandler(t, catalog, forwarder)
	body := `{"model":"text-only","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`
	req := httptest.NewRequest(http.MethodPost, PathResponses, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), errorCodeInputUnsupported) {
		t.Fatalf("image admission = %d %s", rec.Code, rec.Body.String())
	}
	if forwarder.hit {
		t.Fatal("rejected image was forwarded")
	}
}

func TestImageAdmissionAllowsUndeclaredAlias(t *testing.T) {
	catalog := capabilityCatalog{
		byProtocol: map[string][]store.ModelRoute{
			store.ChannelProtocolOpenAIResponses: {
				{ClientModel: "plain", ChannelID: 1, Enabled: true},
			},
		},
	}
	forwarder := &capturedInfo{}
	handler := newCapabilityHandler(t, catalog, forwarder)
	body := `{"model":"plain","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}]}`
	req := httptest.NewRequest(http.MethodPost, PathResponses, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+testClientKey)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !forwarder.hit {
		t.Fatalf("undeclared alias = %d %s", rec.Code, rec.Body.String())
	}
}

func TestBackgroundAndStatefulBoundary(t *testing.T) {
	catalog := capabilityCatalog{
		byProtocol: map[string][]store.ModelRoute{
			store.ChannelProtocolOpenAIResponses: {
				{ClientModel: "single", ChannelID: 7, Enabled: true},
				{ClientModel: "multi", ChannelID: 8, Enabled: true},
				{ClientModel: "multi", ChannelID: 9, Enabled: true},
			},
		},
	}
	post := func(handler *Handler, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, PathResponses, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+testClientKey)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	forwarder := &capturedInfo{}
	handler := newCapabilityHandler(t, catalog, forwarder)
	if rec := post(handler, `{"model":"single","background":true}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), errorCodeUnsupportedParam) {
		t.Fatalf("background = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(handler, `{"model":"multi","previous_response_id":"resp_1"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), errorCodeStatefulRoute) {
		t.Fatalf("multi stateful = %d %s", rec.Code, rec.Body.String())
	}
	if rec := post(handler, `{"model":"single","previous_response_id":"resp_1"}`); rec.Code != http.StatusOK {
		t.Fatalf("single stateful = %d %s", rec.Code, rec.Body.String())
	}
	if !forwarder.hit || forwarder.info.PinnedChannelID != 7 || !forwarder.info.Stateful {
		t.Fatalf("pinned info = %+v", forwarder.info)
	}
	if rec := post(handler, `{"model":"single","input":"hi"}`); rec.Code != http.StatusOK {
		t.Fatalf("plain = %d %s", rec.Code, rec.Body.String())
	}
	if forwarder.info.PinnedChannelID != 0 || forwarder.info.Stateful {
		t.Fatalf("plain request pinned: %+v", forwarder.info)
	}
}
