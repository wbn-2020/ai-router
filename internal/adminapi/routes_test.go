package adminapi

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"ai-router/internal/store"
)

// newModelRoutesAPI 构造带主密钥与登录 cookie 的管理 API；模型映射端点本身
// 不接触主密钥，但测试需要用渠道 API 创建被引用的渠道。
func newModelRoutesAPI(t *testing.T) (*API, *store.Store, *http.Cookie, *bytes.Buffer) {
	t.Helper()
	api, st, _, cookie, logs := newChannelsAPI(t)
	return api, st, cookie, logs
}

func createRouteChannel(
	t *testing.T,
	api *API,
	cookie *http.Cookie,
	name, protocol string,
) channelView {
	t.Helper()
	body := validChannelBody(name)
	body["protocol"] = protocol
	return createChannel(t, api, cookie, body)
}

func validModelRouteBody(clientModel string, channelID int64) map[string]any {
	return map[string]any{
		"client_model":   clientModel,
		"channel_id":     channelID,
		"upstream_model": clientModel + "-upstream",
		"priority":       10,
		"enabled":        true,
	}
}

func createModelRoute(
	t *testing.T,
	api *API,
	cookie *http.Cookie,
	body map[string]any,
) modelRouteView {
	t.Helper()
	rec := apiRequest(t, api, http.MethodPost, "/admin/api/model-routes",
		body, testOrigin, cookie)
	if rec.Code != http.StatusCreated {
		t.Fatalf("创建映射 = %d, want 201（body=%s）", rec.Code, rec.Body.String())
	}
	return decodeBody[modelRouteView](t, rec)
}

func listClientModels(t *testing.T, api *API, cookie *http.Cookie) []string {
	t.Helper()
	rec := apiRequest(t, api, http.MethodGet, "/admin/api/models", nil, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("模型列表 = %d, want 200（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	return decodeBody[modelsResponse](t, rec).Models
}

func containsModel(models []string, target string) bool {
	for _, model := range models {
		if model == target {
			return true
		}
	}
	return false
}

// TestModelRoutesCRUDAPI 覆盖创建、重复冲突、列表/详情、更新、模型列表与删除。
func TestModelRoutesCRUDAPI(t *testing.T) {
	api, st, cookie, logs := newModelRoutesAPI(t)
	respChannel := createRouteChannel(t, api, cookie,
		"responses-channel", store.ChannelProtocolOpenAIResponses)
	chatChannel := createRouteChannel(t, api, cookie,
		"chat-channel", store.ChannelProtocolOpenAIChat)

	created := createModelRoute(t, api, cookie,
		validModelRouteBody("gpt-5", respChannel.ID))
	if created.ID <= 0 || created.ClientModel != "gpt-5" ||
		created.ChannelID != respChannel.ID || created.UpstreamModel != "gpt-5-upstream" ||
		created.Priority != 10 || !created.Enabled {
		t.Fatalf("创建返回 = %+v", created)
	}
	if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Error("创建返回缺少时间戳")
	}

	var enabled, priority int
	if err := st.DB().QueryRow(
		`SELECT priority, enabled FROM model_routes WHERE id = ?`, created.ID,
	).Scan(&priority, &enabled); err != nil {
		t.Fatalf("读取 model_routes: %v", err)
	}
	if priority != 10 || enabled != 1 {
		t.Errorf("落库 priority/enabled = %d/%d，期望 10/1", priority, enabled)
	}

	// 重复 client_model + channel_id 返回 409，不新增行。
	duplicate := apiRequest(t, api, http.MethodPost, "/admin/api/model-routes",
		validModelRouteBody("gpt-5", respChannel.ID), testOrigin, cookie)
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("重复映射 = %d, want 409（body=%s）", duplicate.Code, duplicate.Body.String())
	}
	if !strings.Contains(duplicate.Body.String(), "model_route_conflict") {
		t.Errorf("重复映射响应缺少稳定错误码：%s", duplicate.Body.String())
	}
	assertNoStore(t, duplicate)
	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM model_routes`).Scan(&count); err != nil {
		t.Fatalf("统计 model_routes: %v", err)
	}
	if count != 1 {
		t.Fatalf("重复创建后行数 = %d，期望 1", count)
	}

	// 同一客户端模型可映射到不同渠道。
	chatRoute := createModelRoute(t, api, cookie,
		validModelRouteBody("gpt-5", chatChannel.ID))

	list := apiRequest(t, api, http.MethodGet, "/admin/api/model-routes", nil, "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("映射列表 = %d, want 200（body=%s）", list.Code, list.Body.String())
	}
	assertNoStore(t, list)
	listed := decodeBody[modelRouteListResponse](t, list)
	if len(listed.ModelRoutes) != 2 ||
		listed.ModelRoutes[0].ID != created.ID || listed.ModelRoutes[1].ID != chatRoute.ID {
		t.Fatalf("映射列表 = %+v", listed.ModelRoutes)
	}
	if strings.Contains(list.Body.String(), testChannelKey) ||
		strings.Contains(list.Body.String(), `"key`) {
		t.Fatalf("映射列表泄漏渠道 Key：%s", list.Body.String())
	}

	detail := apiRequest(t, api, http.MethodGet,
		"/admin/api/model-routes/"+intString(created.ID), nil, "", cookie)
	if detail.Code != http.StatusOK {
		t.Fatalf("映射详情 = %d, want 200（body=%s）", detail.Code, detail.Body.String())
	}
	assertNoStore(t, detail)
	if got := decodeBody[modelRouteView](t, detail); got.ID != created.ID {
		t.Errorf("映射详情 ID = %d", got.ID)
	}

	// PATCH 省略字段保持原值。
	patched := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/"+intString(created.ID),
		map[string]any{"upstream_model": "gpt-5-v2", "priority": 3, "enabled": false},
		testOrigin, cookie)
	if patched.Code != http.StatusOK {
		t.Fatalf("更新映射 = %d, want 200（body=%s）", patched.Code, patched.Body.String())
	}
	patchedView := decodeBody[modelRouteView](t, patched)
	if patchedView.ClientModel != "gpt-5" || patchedView.ChannelID != respChannel.ID ||
		patchedView.UpstreamModel != "gpt-5-v2" || patchedView.Priority != 3 ||
		patchedView.Enabled {
		t.Fatalf("更新后 = %+v", patchedView)
	}

	// 更新为已存在的 client_model + channel_id 组合返回 409。
	conflict := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/"+intString(created.ID),
		map[string]any{"channel_id": chatChannel.ID}, testOrigin, cookie)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("冲突更新 = %d, want 409（body=%s）", conflict.Code, conflict.Body.String())
	}

	// /admin/api/models 去重且只列启用映射；gpt-5 仍有 Chat 渠道的启用映射。
	models := listClientModels(t, api, cookie)
	if len(models) != 1 || models[0] != "gpt-5" {
		t.Fatalf("模型列表 = %v，期望 [gpt-5]", models)
	}

	// 新模型禁用后从模型列表消失，重新启用后恢复。
	mini := createModelRoute(t, api, cookie,
		validModelRouteBody("gpt-5-mini", respChannel.ID))
	if !containsModel(listClientModels(t, api, cookie), "gpt-5-mini") {
		t.Fatal("模型列表缺少 gpt-5-mini")
	}
	disableMini := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/"+intString(mini.ID),
		map[string]any{"enabled": false}, testOrigin, cookie)
	if disableMini.Code != http.StatusOK {
		t.Fatalf("禁用 gpt-5-mini = %d（body=%s）", disableMini.Code, disableMini.Body.String())
	}
	if containsModel(listClientModels(t, api, cookie), "gpt-5-mini") {
		t.Fatal("禁用映射后模型仍出现在模型列表")
	}
	enableMini := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/"+intString(mini.ID),
		map[string]any{"enabled": true}, testOrigin, cookie)
	if enableMini.Code != http.StatusOK {
		t.Fatalf("启用 gpt-5-mini = %d（body=%s）", enableMini.Code, enableMini.Body.String())
	}
	if !containsModel(listClientModels(t, api, cookie), "gpt-5-mini") {
		t.Fatal("重新启用映射后模型未回到模型列表")
	}

	// 删除映射后，若模型已无启用路由则从模型列表消失。
	deleted := apiRequest(t, api, http.MethodDelete,
		"/admin/api/model-routes/"+intString(mini.ID), nil, testOrigin, cookie)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("删除映射 = %d, want 204（body=%s）", deleted.Code, deleted.Body.String())
	}
	if containsModel(listClientModels(t, api, cookie), "gpt-5-mini") {
		t.Fatal("删除最后一条映射后模型仍在模型列表")
	}
	missing := apiRequest(t, api, http.MethodGet,
		"/admin/api/model-routes/"+intString(mini.ID), nil, "", cookie)
	if missing.Code != http.StatusNotFound {
		t.Fatalf("删除后详情 = %d, want 404（body=%s）", missing.Code, missing.Body.String())
	}
	missingDelete := apiRequest(t, api, http.MethodDelete,
		"/admin/api/model-routes/"+intString(mini.ID), nil, testOrigin, cookie)
	if missingDelete.Code != http.StatusNotFound {
		t.Fatalf("重复删除 = %d, want 404（body=%s）", missingDelete.Code, missingDelete.Body.String())
	}

	// 删除渠道级联删除其映射。
	if rec := apiRequest(t, api, http.MethodDelete,
		"/admin/api/channels/"+intString(respChannel.ID), nil, testOrigin, cookie); rec.Code != http.StatusNoContent {
		t.Fatalf("删除渠道 = %d, want 204（body=%s）", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet,
		"/admin/api/model-routes/"+intString(created.ID), nil, "", cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("渠道删除后映射详情 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
	}
	if containsModel(listClientModels(t, api, cookie), "gpt-5-mini") {
		t.Fatal("渠道级联后 gpt-5-mini 仍在模型列表")
	}

	logOutput := logs.String()
	if strings.Contains(logOutput, testChannelKey) {
		t.Fatalf("日志泄漏渠道 Key：%s", logOutput)
	}
}

// TestModelRoutesAPIValidation 覆盖非法 channel_id/priority/模型名与默认值。
func TestModelRoutesAPIValidation(t *testing.T) {
	api, st, cookie, _ := newModelRoutesAPI(t)
	channel := createRouteChannel(t, api, cookie,
		"validation-channel", store.ChannelProtocolOpenAIResponses)

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"channel_id 不存在", func(body map[string]any) { body["channel_id"] = 99999 }},
		{"channel_id 为 0", func(body map[string]any) { body["channel_id"] = 0 }},
		{"channel_id 为负", func(body map[string]any) { body["channel_id"] = -1 }},
		{"priority 过大", func(body map[string]any) { body["priority"] = 1001 }},
		{"priority 为负", func(body map[string]any) { body["priority"] = -1 }},
		{"空 client_model", func(body map[string]any) { body["client_model"] = "   " }},
		{"client_model 含控制字符", func(body map[string]any) { body["client_model"] = "gpt\n5" }},
		{"空 upstream_model", func(body map[string]any) { body["upstream_model"] = "" }},
		{"upstream_model 含控制字符", func(body map[string]any) { body["upstream_model"] = "up\x00stream" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := validModelRouteBody("invalid-model", channel.ID)
			tt.mutate(body)
			rec := apiRequest(t, api, http.MethodPost, "/admin/api/model-routes",
				body, testOrigin, cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("状态码 = %d, want 400（body=%s）", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "invalid_model_route") {
				t.Errorf("错误响应缺少稳定错误码：%s", rec.Body.String())
			}
			assertNoStore(t, rec)
		})
	}

	var count int
	if err := st.DB().QueryRow(`SELECT count(*) FROM model_routes`).Scan(&count); err != nil {
		t.Fatalf("统计 model_routes: %v", err)
	}
	if count != 0 {
		t.Fatalf("非法输入落库 %d 行", count)
	}

	// 省略 priority/enabled 时默认 0/true。
	created := createModelRoute(t, api, cookie, map[string]any{
		"client_model":   "defaults",
		"channel_id":     channel.ID,
		"upstream_model": "defaults-upstream",
	})
	if created.Priority != 0 || !created.Enabled {
		t.Fatalf("默认值 = priority %d enabled %v，期望 0/true", created.Priority, created.Enabled)
	}

	// PATCH 的非法值被拒绝且原记录不变。
	badPatch := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/"+intString(created.ID),
		map[string]any{"priority": 1001}, testOrigin, cookie)
	if badPatch.Code != http.StatusBadRequest {
		t.Fatalf("非法 priority 更新 = %d, want 400（body=%s）", badPatch.Code, badPatch.Body.String())
	}
	reloaded := apiRequest(t, api, http.MethodGet,
		"/admin/api/model-routes/"+intString(created.ID), nil, "", cookie)
	if got := decodeBody[modelRouteView](t, reloaded); got.Priority != 0 {
		t.Fatalf("非法更新后 priority = %d，期望保持 0", got.Priority)
	}

	// 不存在/非法 ID。
	if rec := apiRequest(t, api, http.MethodPatch,
		"/admin/api/model-routes/99999",
		map[string]any{"priority": 1}, testOrigin, cookie); rec.Code != http.StatusNotFound {
		t.Fatalf("更新不存在映射 = %d, want 404（body=%s）", rec.Code, rec.Body.String())
	}
	if rec := apiRequest(t, api, http.MethodGet,
		"/admin/api/model-routes/not-a-number", nil, "", cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("非法映射 ID = %d, want 400", rec.Code)
	}
	if rec := apiRequest(t, api, http.MethodDelete,
		"/admin/api/model-routes/0", nil, testOrigin, cookie); rec.Code != http.StatusBadRequest {
		t.Fatalf("ID 为 0 删除 = %d, want 400", rec.Code)
	}
}

// TestModelRoutesRequireSession 覆盖映射与模型列表端点的认证和同源校验。
func TestModelRoutesRequireSession(t *testing.T) {
	api, _, cookie, _ := newModelRoutesAPI(t)

	for _, tt := range []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/admin/api/model-routes"},
		{http.MethodGet, "/admin/api/model-routes/1"},
		{http.MethodGet, "/admin/api/models"},
	} {
		rec := apiRequest(t, api, tt.method, tt.path, nil, "")
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s 未登录 = %d, want 401（body=%s）",
				tt.method, tt.path, rec.Code, rec.Body.String())
		}
		assertNoStore(t, rec)
	}

	noSession := apiRequest(t, api, http.MethodPost, "/admin/api/model-routes",
		validModelRouteBody("gpt-5", 1), testOrigin)
	if noSession.Code != http.StatusUnauthorized {
		t.Fatalf("未登录创建映射 = %d, want 401（body=%s）", noSession.Code, noSession.Body.String())
	}

	crossOrigin := apiRequest(t, api, http.MethodPost, "/admin/api/model-routes",
		validModelRouteBody("gpt-5", 1), "http://evil.example", cookie)
	if crossOrigin.Code != http.StatusForbidden {
		t.Fatalf("跨源创建映射 = %d, want 403（body=%s）", crossOrigin.Code, crossOrigin.Body.String())
	}
}
