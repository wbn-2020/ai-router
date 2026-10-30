package adminapi

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/store"
)

func newObservabilityTestAPI(t *testing.T) (*API, *store.Store, *http.Cookie) {
	t.Helper()
	api, st := newTestAPI(t)
	cookie := loginCookie(t, api, testAdminPassword)
	return api, st, cookie
}

func seedObservabilityAPIChannel(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := st.DB().Exec(`
		INSERT INTO channels (
			name, base_url, protocol, key_ciphertext, key_mask,
			priority, max_concurrency, enabled, created_at, updated_at
		) VALUES (?, 'https://upstream.example.com', 'openai_responses',
			'v1:test', 'sk-***', 0, 1, 1, ?, ?)`,
		name, now, now)
	if err != nil {
		t.Fatalf("seed channel: %v", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatalf("channel LastInsertId: %v", err)
	}
	return id
}

func writeObservabilityRequest(t *testing.T, st *store.Store, log observability.RequestLog) {
	t.Helper()
	if err := observability.New(st.DB()).WriteRequest(context.Background(), log); err != nil {
		t.Fatalf("WriteRequest %s: %v", log.RequestID, err)
	}
}

func observabilityQuery(values url.Values) string {
	return "?" + values.Encode()
}

func testInt64Ptr(value int64) *int64 {
	return &value
}

func testIntPtr(value int) *int {
	return &value
}

func TestObservabilityRequestsAPI(t *testing.T) {
	api, st, cookie := newObservabilityTestAPI(t)
	channelA := seedObservabilityAPIChannel(t, st, "api-channel-a")
	channelB := seedObservabilityAPIChannel(t, st, "api-channel-b")

	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour).Add(10 * time.Minute)
	finished := func(started time.Time, latencyMS int) *time.Time {
		value := started.Add(time.Duration(latencyMS) * time.Millisecond)
		return &value
	}

	logs := []observability.RequestLog{
		{
			RequestID:    "req-api-1",
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        "alpha",
			ChannelID:    &channelA,
			ChannelName:  "api-channel-a",
			Status:       observability.StatusSuccess,
			HTTPStatus:   testIntPtr(200),
			Stream:       true,
			AttemptCount: 1,
			LatencyMS:    100,
			InputTokens:  testInt64Ptr(10),
			OutputTokens: testInt64Ptr(20),
			TotalTokens:  testInt64Ptr(30),
			UsageSource:  "responses.completed",
			StartedAt:    base,
			FinishedAt:   finished(base, 100),
		},
		{
			RequestID:    "req-api-2",
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        "alpha",
			ChannelID:    &channelA,
			ChannelName:  "api-channel-a",
			Status:       observability.StatusError,
			HTTPStatus:   testIntPtr(502),
			ErrorCode:    "upstream_timeout",
			ErrorMessage: "上游超时",
			Stream:       true,
			AttemptCount: 2,
			RetryCount:   1,
			LatencyMS:    250,
			StartedAt:    base.Add(time.Minute),
			FinishedAt:   finished(base.Add(time.Minute), 250),
		},
		{
			RequestID:    "req-api-3",
			Protocol:     "openai_chat",
			Endpoint:     "/v1/chat/completions",
			Model:        "beta",
			ChannelID:    &channelB,
			ChannelName:  "api-channel-b",
			Status:       observability.StatusSuccess,
			HTTPStatus:   testIntPtr(200),
			AttemptCount: 1,
			LatencyMS:    150,
			StartedAt:    base.Add(2 * time.Minute),
			FinishedAt:   finished(base.Add(2*time.Minute), 150),
		},
	}
	for _, log := range logs {
		writeObservabilityRequest(t, st, log)
	}
	if err := observability.New(st.DB()).WriteAttempt(context.Background(), observability.RequestAttempt{
		RequestID:     "req-api-2",
		AttemptIndex:  1,
		ChannelID:     &channelA,
		ChannelName:   "api-channel-a",
		UpstreamModel: "alpha-upstream",
		Status:        observability.StatusError,
		HTTPStatus:    testIntPtr(503),
		ErrorCode:     "upstream_unavailable",
		ErrorMessage:  "上游暂不可用",
		LatencyMS:     80,
		StartedAt:     base.Add(time.Minute),
		FinishedAt:    finished(base.Add(time.Minute), 80),
	}); err != nil {
		t.Fatalf("WriteAttempt 1: %v", err)
	}
	if err := observability.New(st.DB()).WriteAttempt(context.Background(), observability.RequestAttempt{
		RequestID:     "req-api-2",
		AttemptIndex:  2,
		ChannelID:     &channelA,
		ChannelName:   "api-channel-a",
		UpstreamModel: "alpha-upstream",
		Status:        observability.StatusError,
		HTTPStatus:    testIntPtr(502),
		ErrorCode:     "upstream_timeout",
		ErrorMessage:  "上游超时",
		LatencyMS:     170,
		StartedAt:     base.Add(time.Minute),
		FinishedAt:    finished(base.Add(time.Minute), 170),
	}); err != nil {
		t.Fatalf("WriteAttempt 2: %v", err)
	}

	rangeValues := url.Values{}
	rangeValues.Set("from", base.Format(time.RFC3339Nano))
	rangeValues.Set("to", base.Add(time.Hour).Format(time.RFC3339Nano))

	list := apiRequest(t, api, http.MethodGet,
		"/admin/api/requests"+observabilityQuery(rangeValues), nil, "", cookie)
	if list.Code != http.StatusOK {
		t.Fatalf("请求列表 = %d，期望 200（body=%s）", list.Code, list.Body.String())
	}
	assertNoStore(t, list)
	raw := list.Body.String()
	for _, forbidden := range []string{
		`"authorization"`, `"headers"`, `"prompt"`, `"request_body"`, `"response_body"`,
	} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("请求列表包含敏感字段 %s：%s", forbidden, raw)
		}
	}
	if !strings.Contains(raw, `"input_tokens":null`) {
		t.Fatalf("缺失 usage 时应返回 input_tokens:null：%s", raw)
	}
	listed := decodeBody[requestListResponse](t, list)
	if listed.Total != 3 || len(listed.Requests) != 3 {
		t.Fatalf("列表 total/len = %d/%d", listed.Total, len(listed.Requests))
	}
	if listed.Requests[0].RequestID != "req-api-3" ||
		listed.Requests[1].RequestID != "req-api-2" ||
		listed.Requests[2].RequestID != "req-api-1" {
		t.Fatalf("列表顺序 = %+v", listed.Requests)
	}
	if listed.Requests[2].InputTokens == nil || *listed.Requests[2].InputTokens != 10 {
		t.Fatalf("req-api-1 tokens = %v", listed.Requests[2].InputTokens)
	}
	if listed.Requests[0].InputTokens != nil || listed.Requests[0].OutputTokens != nil {
		t.Fatalf("req-api-3 缺失 usage 应为 nil：%+v", listed.Requests[0])
	}

	filterCases := []struct {
		name  string
		key   string
		value string
		want  int64
	}{
		{name: "模型", key: "model", value: "alpha", want: 2},
		{name: "渠道", key: "channel_id", value: fmt.Sprint(channelA), want: 2},
		{name: "状态", key: "status", value: observability.StatusError, want: 1},
		{name: "错误码", key: "error_code", value: "upstream_timeout", want: 1},
	}
	for _, tc := range filterCases {
		t.Run("filter_"+tc.name, func(t *testing.T) {
			values := url.Values{}
			values.Set("from", base.Format(time.RFC3339Nano))
			values.Set("to", base.Add(time.Hour).Format(time.RFC3339Nano))
			values.Set(tc.key, tc.value)
			rec := apiRequest(t, api, http.MethodGet,
				"/admin/api/requests"+observabilityQuery(values), nil, "", cookie)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s过滤 = %d（body=%s）", tc.name, rec.Code, rec.Body.String())
			}
			body := decodeBody[requestListResponse](t, rec)
			if body.Total != tc.want || int64(len(body.Requests)) != tc.want {
				t.Fatalf("%s过滤 total/len = %d/%d，期望 %d",
					tc.name, body.Total, len(body.Requests), tc.want)
			}
		})
	}

	paginationValues := rangeValues
	paginationValues.Set("limit", "1")
	paginationValues.Set("offset", "1")
	paged := apiRequest(t, api, http.MethodGet,
		"/admin/api/requests"+observabilityQuery(paginationValues), nil, "", cookie)
	if paged.Code != http.StatusOK {
		t.Fatalf("分页 = %d（body=%s）", paged.Code, paged.Body.String())
	}
	pagedBody := decodeBody[requestListResponse](t, paged)
	if pagedBody.Total != 3 || pagedBody.Limit != 1 || pagedBody.Offset != 1 ||
		len(pagedBody.Requests) != 1 || pagedBody.Requests[0].RequestID != "req-api-2" {
		t.Fatalf("分页结果 = %+v", pagedBody)
	}

	invalidCases := []struct {
		name string
		path string
		code string
	}{
		{
			name: "非法 from",
			path: "/admin/api/requests?from=not-a-time",
			code: "invalid_time_range",
		},
		{
			name: "from 晚于 to",
			path: "/admin/api/requests?from=" + url.QueryEscape(base.Add(time.Hour).Format(time.RFC3339Nano)) +
				"&to=" + url.QueryEscape(base.Format(time.RFC3339Nano)),
			code: "invalid_time_range",
		},
		{
			name: "非法 channel_id",
			path: "/admin/api/requests?channel_id=0",
			code: "invalid_channel_filter",
		},
		{
			name: "非法 limit",
			path: "/admin/api/requests?limit=0",
			code: "invalid_pagination",
		},
	}
	for _, tc := range invalidCases {
		t.Run(tc.name, func(t *testing.T) {
			rec := apiRequest(t, api, http.MethodGet, tc.path, nil, "", cookie)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d，期望 400（body=%s）", tc.name, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.code) {
				t.Fatalf("%s 缺少错误码 %s：%s", tc.name, tc.code, rec.Body.String())
			}
		})
	}

	unauthorized := apiRequest(t, api, http.MethodGet, "/admin/api/requests", nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录请求列表 = %d，期望 401", unauthorized.Code)
	}

	detail := apiRequest(t, api, http.MethodGet, "/admin/api/requests/req-api-2", nil, "", cookie)
	if detail.Code != http.StatusOK {
		t.Fatalf("请求详情 = %d（body=%s）", detail.Code, detail.Body.String())
	}
	assertNoStore(t, detail)
	detailBody := decodeBody[requestDetailResponse](t, detail)
	if detailBody.Request.RequestID != "req-api-2" ||
		detailBody.Request.ErrorCode == nil ||
		*detailBody.Request.ErrorCode != "upstream_timeout" {
		t.Fatalf("详情 request = %+v", detailBody.Request)
	}
	if len(detailBody.Attempts) != 2 {
		t.Fatalf("详情 attempts = %d，期望 2", len(detailBody.Attempts))
	}
	if detailBody.Attempts[0].AttemptIndex != 1 ||
		detailBody.Attempts[0].HTTPStatus == nil ||
		*detailBody.Attempts[0].HTTPStatus != 503 ||
		detailBody.Attempts[0].ErrorCode == nil ||
		*detailBody.Attempts[0].ErrorCode != "upstream_unavailable" {
		t.Fatalf("attempt 1 = %+v", detailBody.Attempts[0])
	}
	if detailBody.Attempts[1].AttemptIndex != 2 ||
		detailBody.Attempts[1].HTTPStatus == nil ||
		*detailBody.Attempts[1].HTTPStatus != 502 {
		t.Fatalf("attempt 2 = %+v", detailBody.Attempts[1])
	}

	missing := apiRequest(t, api, http.MethodGet, "/admin/api/requests/req-missing", nil, "", cookie)
	if missing.Code != http.StatusNotFound ||
		!strings.Contains(missing.Body.String(), "request_not_found") {
		t.Fatalf("缺失详情 = %d（body=%s）", missing.Code, missing.Body.String())
	}
}

func TestObservabilityRequiresDatabase(t *testing.T) {
	api, _ := newTestAPI(t, func(opts *Options) {
		opts.DB = nil
	})
	cookie := loginCookie(t, api, testAdminPassword)

	for _, path := range []string{"/admin/api/requests", "/admin/api/stats"} {
		rec := apiRequest(t, api, http.MethodGet, path, nil, "", cookie)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s = %d，期望 503（body=%s）", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "observability_unavailable") {
			t.Fatalf("%s 缺少 observability_unavailable：%s", path, rec.Body.String())
		}
	}
}

func TestObservabilityStatsAPI(t *testing.T) {
	api, st, cookie := newObservabilityTestAPI(t)
	channelA := seedObservabilityAPIChannel(t, st, "stats-channel-a")
	channelB := seedObservabilityAPIChannel(t, st, "stats-channel-b")

	base := time.Now().UTC().Truncate(time.Hour).Add(-2 * time.Hour).Add(10 * time.Minute)
	finished := func(started time.Time, latencyMS int) *time.Time {
		value := started.Add(time.Duration(latencyMS) * time.Millisecond)
		return &value
	}
	logs := []observability.RequestLog{
		{
			RequestID:    "req-stats-1",
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        "alpha",
			ChannelID:    &channelA,
			ChannelName:  "stats-channel-a",
			Status:       observability.StatusSuccess,
			HTTPStatus:   testIntPtr(200),
			AttemptCount: 1,
			LatencyMS:    100,
			InputTokens:  testInt64Ptr(10),
			OutputTokens: testInt64Ptr(20),
			TotalTokens:  testInt64Ptr(30),
			UsageSource:  "responses.completed",
			StartedAt:    base,
			FinishedAt:   finished(base, 100),
		},
		{
			RequestID:    "req-stats-2",
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        "alpha",
			ChannelID:    &channelA,
			ChannelName:  "stats-channel-a",
			Status:       observability.StatusError,
			HTTPStatus:   testIntPtr(502),
			ErrorCode:    "upstream_timeout",
			AttemptCount: 1,
			LatencyMS:    250,
			StartedAt:    base.Add(time.Minute),
			FinishedAt:   finished(base.Add(time.Minute), 250),
		},
		{
			RequestID:    "req-stats-3",
			Protocol:     "openai_chat",
			Endpoint:     "/v1/chat/completions",
			Model:        "beta",
			ChannelID:    &channelB,
			ChannelName:  "stats-channel-b",
			Status:       observability.StatusSuccess,
			HTTPStatus:   testIntPtr(200),
			AttemptCount: 1,
			LatencyMS:    150,
			InputTokens:  testInt64Ptr(5),
			OutputTokens: testInt64Ptr(7),
			TotalTokens:  testInt64Ptr(12),
			UsageSource:  "chat.completion",
			StartedAt:    base.Add(2 * time.Minute),
			FinishedAt:   finished(base.Add(2*time.Minute), 150),
		},
		{
			RequestID:    "req-stats-4",
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        "gamma",
			ChannelID:    &channelB,
			ChannelName:  "stats-channel-b",
			Status:       observability.StatusSuccess,
			HTTPStatus:   testIntPtr(200),
			AttemptCount: 1,
			LatencyMS:    300,
			StartedAt:    base.Add(3 * time.Minute),
			FinishedAt:   finished(base.Add(3*time.Minute), 300),
		},
	}
	for _, log := range logs {
		writeObservabilityRequest(t, st, log)
	}

	values := url.Values{}
	values.Set("from", base.Format(time.RFC3339Nano))
	values.Set("to", base.Add(time.Hour).Format(time.RFC3339Nano))

	rec := apiRequest(t, api, http.MethodGet,
		"/admin/api/stats"+observabilityQuery(values), nil, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("统计 = %d（body=%s）", rec.Code, rec.Body.String())
	}
	assertNoStore(t, rec)
	raw := rec.Body.String()
	for _, forbidden := range []string{
		`"authorization"`, `"headers"`, `"prompt"`, `"request_body"`, `"response_body"`,
	} {
		if strings.Contains(strings.ToLower(raw), forbidden) {
			t.Fatalf("统计包含敏感字段 %s：%s", forbidden, raw)
		}
	}

	stats := decodeBody[statsResponse](t, rec)
	if !stats.From.Equal(base) || !stats.To.Equal(base.Add(time.Hour)) {
		t.Fatalf("统计时间 = %v/%v", stats.From, stats.To)
	}
	totals := stats.Totals
	if totals.RequestCount != 4 || totals.SuccessCount != 3 ||
		totals.ErrorCount != 1 || totals.UsageCount != 2 {
		t.Fatalf("totals 计数 = %+v", totals)
	}
	if totals.ErrorRate == nil || *totals.ErrorRate != 0.25 {
		t.Fatalf("error_rate = %v，期望 0.25", totals.ErrorRate)
	}
	if totals.InputTokens == nil || *totals.InputTokens != 15 ||
		totals.OutputTokens == nil || *totals.OutputTokens != 27 ||
		totals.TotalTokens == nil || *totals.TotalTokens != 42 {
		t.Fatalf("totals tokens = %v/%v/%v",
			totals.InputTokens, totals.OutputTokens, totals.TotalTokens)
	}
	if totals.AvgLatencyMS == nil || *totals.AvgLatencyMS != 200 {
		t.Fatalf("avg_latency_ms = %v，期望 200", totals.AvgLatencyMS)
	}
	if totals.P95LatencyMS == nil || *totals.P95LatencyMS != 300 {
		t.Fatalf("p95_latency_ms = %v，期望 300", totals.P95LatencyMS)
	}

	if len(stats.Hourly) != 1 {
		t.Fatalf("hourly 桶数 = %d，期望 1", len(stats.Hourly))
	}
	if stats.Hourly[0].Bucket != base.Truncate(time.Hour).Format(time.RFC3339) ||
		stats.Hourly[0].RequestCount != 4 {
		t.Fatalf("hourly[0] = %+v", stats.Hourly[0])
	}

	byModel := make(map[string]statsPoint, len(stats.ByModel))
	for _, point := range stats.ByModel {
		byModel[point.Model] = point
	}
	if len(byModel) != 3 {
		t.Fatalf("by_model 数 = %d，期望 3", len(byModel))
	}
	alpha := byModel["alpha"]
	if alpha.RequestCount != 2 || alpha.SuccessCount != 1 || alpha.ErrorCount != 1 ||
		alpha.ErrorRate == nil || *alpha.ErrorRate != 0.5 ||
		alpha.P95LatencyMS == nil || *alpha.P95LatencyMS != 250 {
		t.Fatalf("alpha 聚合 = %+v", alpha)
	}
	if gamma := byModel["gamma"]; gamma.InputTokens != nil || gamma.OutputTokens != nil ||
		gamma.TotalTokens != nil || gamma.UsageCount != 0 {
		t.Fatalf("gamma 无 usage 的 token 应为 nil：%+v", gamma)
	}

	byChannel := make(map[string]statsPoint, len(stats.ByChannel))
	for _, point := range stats.ByChannel {
		byChannel[point.ChannelName] = point
	}
	if len(byChannel) != 2 {
		t.Fatalf("by_channel 数 = %d，期望 2", len(byChannel))
	}
	if byChannel["stats-channel-b"].RequestCount != 2 {
		t.Fatalf("channel-b 聚合 = %+v", byChannel["stats-channel-b"])
	}

	filteredValues := url.Values{}
	filteredValues.Set("from", base.Format(time.RFC3339Nano))
	filteredValues.Set("to", base.Add(time.Hour).Format(time.RFC3339Nano))
	filteredValues.Set("model", "alpha")
	filtered := apiRequest(t, api, http.MethodGet,
		"/admin/api/stats"+observabilityQuery(filteredValues), nil, "", cookie)
	if filtered.Code != http.StatusOK {
		t.Fatalf("按模型统计 = %d（body=%s）", filtered.Code, filtered.Body.String())
	}
	filteredStats := decodeBody[statsResponse](t, filtered)
	if filteredStats.Totals.RequestCount != 2 ||
		filteredStats.Totals.ErrorCount != 1 ||
		len(filteredStats.ByModel) != 1 ||
		len(filteredStats.ByChannel) != 1 ||
		filteredStats.ByChannel[0].ChannelName != "stats-channel-a" {
		t.Fatalf("按模型统计 = %+v", filteredStats)
	}

	noUsageValues := url.Values{}
	noUsageValues.Set("from", base.Format(time.RFC3339Nano))
	noUsageValues.Set("to", base.Add(time.Hour).Format(time.RFC3339Nano))
	noUsageValues.Set("model", "gamma")
	noUsage := apiRequest(t, api, http.MethodGet,
		"/admin/api/stats"+observabilityQuery(noUsageValues), nil, "", cookie)
	if noUsage.Code != http.StatusOK {
		t.Fatalf("无 usage 统计 = %d（body=%s）", noUsage.Code, noUsage.Body.String())
	}
	if !strings.Contains(noUsage.Body.String(), `"input_tokens":null`) ||
		!strings.Contains(noUsage.Body.String(), `"output_tokens":null`) ||
		!strings.Contains(noUsage.Body.String(), `"p95_latency_ms":300`) {
		t.Fatalf("无 usage 统计应保留 null token：%s", noUsage.Body.String())
	}

	rangeHit := apiRequest(t, api, http.MethodGet, "/admin/api/stats?range=24h", nil, "", cookie)
	if rangeHit.Code != http.StatusOK {
		t.Fatalf("range=24h = %d（body=%s）", rangeHit.Code, rangeHit.Body.String())
	}
	rangeStats := decodeBody[statsResponse](t, rangeHit)
	if rangeStats.Totals.RequestCount != 4 {
		t.Fatalf("range=24h 请求数 = %d，期望 4", rangeStats.Totals.RequestCount)
	}

	invalid := apiRequest(t, api, http.MethodGet, "/admin/api/stats?range=nope", nil, "", cookie)
	if invalid.Code != http.StatusBadRequest ||
		!strings.Contains(invalid.Body.String(), "invalid_range") {
		t.Fatalf("非法 range = %d（body=%s）", invalid.Code, invalid.Body.String())
	}

	unauthorized := apiRequest(t, api, http.MethodGet, "/admin/api/stats", nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("未登录统计 = %d，期望 401", unauthorized.Code)
	}
}
