package observability

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	storepkg "ai-router/internal/store"
)

var testBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func newTestObservability(t *testing.T) (*Store, *storepkg.Store) {
	t.Helper()
	dir := t.TempDir()
	st, err := storepkg.Open(storepkg.Paths{
		DataDir: dir,
		DBPath:  filepath.Join(dir, "gateway.db"),
	})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		_ = st.Close()
	})
	if _, err := st.Migrate(context.Background()); err != nil {
		t.Fatalf("store.Migrate: %v", err)
	}
	return New(st.DB()), st
}

func seedObservabilityChannel(t *testing.T, st *storepkg.Store, name string) int64 {
	t.Helper()
	now := formatTime(testBase)
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

func testInt64Ptr(value int64) *int64 {
	return &value
}

func testIntPtr(value int) *int {
	return &value
}

func testTimePtr(value time.Time) *time.Time {
	return &value
}

func testRequestLog(requestID string, channelID int64, model string, startedAt time.Time) RequestLog {
	return RequestLog{
		RequestID:         requestID,
		Protocol:          "openai_responses",
		Endpoint:          "/v1/responses",
		Model:             model,
		ChannelID:         &channelID,
		ChannelName:       "channel-" + fmt.Sprint(channelID),
		Status:            StatusSuccess,
		HTTPStatus:        testIntPtr(200),
		Stream:            true,
		AttemptCount:      1,
		LatencyMS:         100,
		SemanticCommitted: true,
		StartedAt:         startedAt,
		FinishedAt:        testTimePtr(startedAt.Add(100 * time.Millisecond)),
	}
}

func TestRequestLogSchemaHasNoSensitiveColumns(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "schema-check")

	for _, table := range []string{"request_logs", "request_attempts"} {
		rows, err := st.DB().Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
		if err != nil {
			t.Fatalf("PRAGMA table_info(%s): %v", table, err)
		}
		columns := make([]string, 0, 24)
		for rows.Next() {
			var (
				cid        int
				name       string
				columnType string
				notNull    int
				defaultVal any
				pk         int
			)
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultVal, &pk); err != nil {
				rows.Close()
				t.Fatalf("读取 %s 列信息: %v", table, err)
			}
			columns = append(columns, strings.ToLower(name))
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("遍历 %s 列信息: %v", table, err)
		}
		rows.Close()

		forbidden := []string{"header", "body", "prompt", "authorization", "cookie", "password", "secret"}
		for _, column := range columns {
			for _, fragment := range forbidden {
				if strings.Contains(column, fragment) {
					t.Errorf("%s 含敏感列 %q：日志只能保存元数据", table, column)
				}
			}
		}
	}

	log := testRequestLog("req-schema", channelID, "gpt-5", testBase)
	if err := obs.WriteRequest(context.Background(), log); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	var captures int
	if err := st.DB().QueryRow(`SELECT count(*) FROM request_captures`).Scan(&captures); err != nil {
		t.Fatalf("统计 captures: %v", err)
	}
	if captures != 0 {
		t.Fatalf("默认写请求日志产生了 %d 条捕获行，期望 0", captures)
	}
}

func TestWriteRequestAndAttemptsRoundTrip(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "roundtrip")
	started := testBase
	finished := started.Add(1500 * time.Millisecond)

	log := RequestLog{
		RequestID:         "req-roundtrip",
		Protocol:          "openai_responses",
		Endpoint:          "/v1/responses",
		Model:             "gpt-5",
		ChannelID:         &channelID,
		ChannelName:       "roundtrip",
		Status:            StatusSuccess,
		HTTPStatus:        testIntPtr(200),
		Stream:            true,
		AttemptCount:      2,
		RetryCount:        1,
		TTFTMS:            testInt64Ptr(120),
		LatencyMS:         1500,
		InputTokens:       testInt64Ptr(12),
		OutputTokens:      testInt64Ptr(34),
		TotalTokens:       testInt64Ptr(46),
		UsageSource:       "responses.completed",
		SemanticCommitted: true,
		ResponseBytes:     testInt64Ptr(2048),
		StartedAt:         started,
		FinishedAt:        &finished,
	}
	if err := obs.WriteRequest(context.Background(), log); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	firstStarted := started
	firstFinished := started.Add(300 * time.Millisecond)
	if err := obs.WriteAttempt(context.Background(), RequestAttempt{
		RequestID:         "req-roundtrip",
		AttemptIndex:      1,
		ChannelID:         &channelID,
		ChannelName:       "roundtrip",
		UpstreamModel:     "gpt-5",
		Status:            StatusError,
		HTTPStatus:        testIntPtr(503),
		ErrorCode:         "upstream_unavailable",
		ErrorMessage:      "上游暂不可用",
		LatencyMS:         300,
		SemanticCommitted: false,
		StartedAt:         firstStarted,
		FinishedAt:        &firstFinished,
	}); err != nil {
		t.Fatalf("WriteAttempt 1: %v", err)
	}
	secondStarted := started.Add(300 * time.Millisecond)
	if err := obs.WriteAttempt(context.Background(), RequestAttempt{
		RequestID:         "req-roundtrip",
		AttemptIndex:      2,
		ChannelID:         &channelID,
		ChannelName:       "roundtrip",
		UpstreamModel:     "gpt-5",
		Status:            StatusSuccess,
		HTTPStatus:        testIntPtr(200),
		LatencyMS:         1200,
		SemanticCommitted: true,
		StartedAt:         secondStarted,
		FinishedAt:        &finished,
	}); err != nil {
		t.Fatalf("WriteAttempt 2: %v", err)
	}

	detail, err := obs.GetRequest(context.Background(), "req-roundtrip")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	got := detail.Request
	if got.RequestID != "req-roundtrip" || got.Protocol != "openai_responses" ||
		got.Endpoint != "/v1/responses" || got.Model != "gpt-5" {
		t.Fatalf("请求元数据 = %+v", got)
	}
	if got.ChannelID == nil || *got.ChannelID != channelID || got.ChannelName != "roundtrip" {
		t.Fatalf("渠道快照 = %v/%q", got.ChannelID, got.ChannelName)
	}
	if got.Status != StatusSuccess || got.HTTPStatus == nil || *got.HTTPStatus != 200 {
		t.Fatalf("状态 = %q/%v", got.Status, got.HTTPStatus)
	}
	if !got.Stream || got.AttemptCount != 2 || got.RetryCount != 1 {
		t.Fatalf("stream/attempt/retry = %v/%d/%d", got.Stream, got.AttemptCount, got.RetryCount)
	}
	if got.TTFTMS == nil || *got.TTFTMS != 120 || got.LatencyMS != 1500 {
		t.Fatalf("ttft/latency = %v/%d", got.TTFTMS, got.LatencyMS)
	}
	if got.InputTokens == nil || *got.InputTokens != 12 ||
		got.OutputTokens == nil || *got.OutputTokens != 34 ||
		got.TotalTokens == nil || *got.TotalTokens != 46 {
		t.Fatalf("tokens = %v/%v/%v", got.InputTokens, got.OutputTokens, got.TotalTokens)
	}
	if got.UsageSource != "responses.completed" || !got.SemanticCommitted ||
		got.ResponseBytes == nil || *got.ResponseBytes != 2048 {
		t.Fatalf("usage/commit/bytes = %q/%v/%v",
			got.UsageSource, got.SemanticCommitted, got.ResponseBytes)
	}
	if !got.StartedAt.Equal(started) || got.FinishedAt == nil || !got.FinishedAt.Equal(finished) {
		t.Fatalf("时间 = %v/%v", got.StartedAt, got.FinishedAt)
	}

	if len(detail.Attempts) != 2 {
		t.Fatalf("attempt 数 = %d，期望 2", len(detail.Attempts))
	}
	first := detail.Attempts[0]
	if first.AttemptIndex != 1 || first.Status != StatusError ||
		first.HTTPStatus == nil || *first.HTTPStatus != 503 ||
		first.ErrorCode != "upstream_unavailable" || first.SemanticCommitted {
		t.Fatalf("attempt 1 = %+v", first)
	}
	second := detail.Attempts[1]
	if second.AttemptIndex != 2 || second.Status != StatusSuccess ||
		second.ChannelName != "roundtrip" || !second.SemanticCommitted {
		t.Fatalf("attempt 2 = %+v", second)
	}
}

func TestListRequestsFiltersAndPagination(t *testing.T) {
	obs, st := newTestObservability(t)
	channelA := seedObservabilityChannel(t, st, "channel-a")
	channelB := seedObservabilityChannel(t, st, "channel-b")
	ctx := context.Background()

	logs := []RequestLog{
		testRequestLog("req-1", channelA, "alpha", testBase),
		testRequestLog("req-2", channelA, "alpha", testBase.Add(time.Minute)),
		testRequestLog("req-3", channelB, "beta", testBase.Add(2*time.Minute)),
		testRequestLog("req-4", channelB, "beta", testBase.Add(3*time.Minute)),
	}
	logs[1].Status = StatusError
	logs[1].HTTPStatus = testIntPtr(502)
	logs[1].ErrorCode = "upstream_timeout"
	logs[1].ErrorMessage = "上游超时"
	logs[3].Status = StatusCancelled
	logs[3].HTTPStatus = testIntPtr(499)
	logs[3].ErrorCode = "client_closed"
	for _, log := range logs {
		if err := obs.WriteRequest(ctx, log); err != nil {
			t.Fatalf("WriteRequest %s: %v", log.RequestID, err)
		}
	}

	page, err := obs.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	if page.Total != 4 || len(page.Requests) != 4 {
		t.Fatalf("无过滤 total/len = %d/%d", page.Total, len(page.Requests))
	}
	wantOrder := []string{"req-4", "req-3", "req-2", "req-1"}
	for i, want := range wantOrder {
		if page.Requests[i].RequestID != want {
			t.Fatalf("倒序第 %d 条 = %s，期望 %s", i, page.Requests[i].RequestID, want)
		}
	}

	alphaPage, err := obs.ListRequests(ctx, RequestFilter{Model: "alpha"})
	if err != nil {
		t.Fatalf("按模型过滤: %v", err)
	}
	if alphaPage.Total != 2 || len(alphaPage.Requests) != 2 {
		t.Fatalf("模型 alpha total/len = %d/%d", alphaPage.Total, len(alphaPage.Requests))
	}

	channelBPage, err := obs.ListRequests(ctx, RequestFilter{ChannelID: &channelB})
	if err != nil {
		t.Fatalf("按渠道过滤: %v", err)
	}
	if channelBPage.Total != 2 {
		t.Fatalf("渠道 B total = %d，期望 2", channelBPage.Total)
	}

	errorPage, err := obs.ListRequests(ctx, RequestFilter{Status: StatusError})
	if err != nil {
		t.Fatalf("按状态过滤: %v", err)
	}
	if errorPage.Total != 1 || errorPage.Requests[0].RequestID != "req-2" ||
		errorPage.Requests[0].ErrorCode != "upstream_timeout" {
		t.Fatalf("错误过滤 = %+v", errorPage.Requests)
	}

	codePage, err := obs.ListRequests(ctx, RequestFilter{ErrorCode: "client_closed"})
	if err != nil {
		t.Fatalf("按错误码过滤: %v", err)
	}
	if codePage.Total != 1 || codePage.Requests[0].RequestID != "req-4" {
		t.Fatalf("错误码过滤 = %+v", codePage.Requests)
	}

	rangePage, err := obs.ListRequests(ctx, RequestFilter{
		From: testBase.Add(time.Minute),
		To:   testBase.Add(3 * time.Minute),
	})
	if err != nil {
		t.Fatalf("按时间过滤: %v", err)
	}
	if rangePage.Total != 2 ||
		rangePage.Requests[0].RequestID != "req-3" ||
		rangePage.Requests[1].RequestID != "req-2" {
		t.Fatalf("时间过滤 = %+v", rangePage.Requests)
	}

	page, err = obs.ListRequests(ctx, RequestFilter{Limit: 2, Offset: 1})
	if err != nil {
		t.Fatalf("分页: %v", err)
	}
	if page.Total != 4 || page.Limit != 2 || page.Offset != 1 || len(page.Requests) != 2 {
		t.Fatalf("分页元数据 = %+v", page)
	}
	if page.Requests[0].RequestID != "req-3" || page.Requests[1].RequestID != "req-2" {
		t.Fatalf("分页结果 = %+v", page.Requests)
	}

	emptyPage, err := obs.ListRequests(ctx, RequestFilter{Model: "missing"})
	if err != nil {
		t.Fatalf("空结果: %v", err)
	}
	if emptyPage.Total != 0 || len(emptyPage.Requests) != 0 {
		t.Fatalf("空结果 = %+v", emptyPage)
	}

	if _, err := obs.ListRequests(ctx, RequestFilter{
		From: testBase.Add(time.Hour),
		To:   testBase,
	}); err != ErrInvalidTimeRange {
		t.Fatalf("非法时间范围 err = %v，期望 ErrInvalidTimeRange", err)
	}
}

func TestUsageMissingIsNull(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "null-usage")
	ctx := context.Background()

	if err := obs.WriteRequest(ctx,
		testRequestLog("req-null", channelID, "gpt-5", testBase)); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}

	page, err := obs.ListRequests(ctx, RequestFilter{})
	if err != nil {
		t.Fatalf("ListRequests: %v", err)
	}
	log := page.Requests[0]
	if log.InputTokens != nil || log.OutputTokens != nil || log.TotalTokens != nil {
		t.Fatalf("缺失 usage 的 token 应为 nil，实际 %v/%v/%v",
			log.InputTokens, log.OutputTokens, log.TotalTokens)
	}
	if log.UsageSource != "" {
		t.Fatalf("缺失 usage 时 usage_source 应为空，实际 %q", log.UsageSource)
	}

	stats, err := obs.Stats(ctx, StatsFilter{
		From: testBase.Add(-time.Hour),
		To:   testBase.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Totals.InputTokens != nil || stats.Totals.OutputTokens != nil ||
		stats.Totals.TotalTokens != nil {
		t.Fatalf("全缺失 usage 时统计 token 应为 nil，实际 %v/%v/%v",
			stats.Totals.InputTokens, stats.Totals.OutputTokens, stats.Totals.TotalTokens)
	}
	if stats.Totals.UsageCount != 0 {
		t.Fatalf("usage_count = %d，期望 0", stats.Totals.UsageCount)
	}
	if stats.Totals.RequestCount != 1 || stats.Totals.SuccessCount != 1 ||
		stats.Totals.ErrorCount != 0 {
		t.Fatalf("totals = %+v", stats.Totals)
	}
	if stats.Totals.ErrorRate == nil || *stats.Totals.ErrorRate != 0 {
		t.Fatalf("error_rate = %v，期望 0", stats.Totals.ErrorRate)
	}
}

func TestStatsAggregation(t *testing.T) {
	obs, st := newTestObservability(t)
	channelA := seedObservabilityChannel(t, st, "primary")
	channelB := seedObservabilityChannel(t, st, "secondary")
	ctx := context.Background()

	for i := 1; i <= 20; i++ {
		model := "gpt-5"
		channelID := channelA
		channelName := "primary"
		if i > 10 {
			model = "gpt-4"
			channelID = channelB
			channelName = "secondary"
		}
		status := StatusSuccess
		httpStatus := 200
		errorCode := ""
		if i%5 == 0 {
			status = StatusError
			httpStatus = 502
			errorCode = "upstream_error"
			if i%10 == 0 {
				status = StatusCancelled
				httpStatus = 499
				errorCode = "client_closed"
			}
		}
		var input, output, total *int64
		if i%2 == 0 {
			input = testInt64Ptr(int64(i))
			output = testInt64Ptr(int64(2 * i))
			total = testInt64Ptr(int64(3 * i))
		}
		startedAt := testBase.Add(time.Duration(i) * time.Minute)
		log := RequestLog{
			RequestID:    fmt.Sprintf("req-%02d", i),
			Protocol:     "openai_responses",
			Endpoint:     "/v1/responses",
			Model:        model,
			ChannelID:    &channelID,
			ChannelName:  channelName,
			Status:       status,
			HTTPStatus:   testIntPtr(httpStatus),
			ErrorCode:    errorCode,
			AttemptCount: 1,
			LatencyMS:    int64(i * 10),
			InputTokens:  input,
			OutputTokens: output,
			TotalTokens:  total,
			UsageSource: func() string {
				if input != nil {
					return "responses.completed"
				}
				return ""
			}(),
			StartedAt: startedAt,
		}
		if err := obs.WriteRequest(ctx, log); err != nil {
			t.Fatalf("WriteRequest %s: %v", log.RequestID, err)
		}
	}

	stats, err := obs.Stats(ctx, StatsFilter{
		From: testBase.Add(-time.Hour),
		To:   testBase.Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	totals := stats.Totals
	if totals.RequestCount != 20 || totals.SuccessCount != 16 ||
		totals.ErrorCount != 4 || totals.UsageCount != 10 {
		t.Fatalf("totals 计数 = %+v", totals)
	}
	assertFloatValue(t, "totals.error_rate", totals.ErrorRate, 0.2)
	assertFloatValue(t, "totals.avg_latency_ms", totals.AvgLatencyMS, 105)
	if totals.InputTokens == nil || *totals.InputTokens != 110 {
		t.Fatalf("totals.input_tokens = %v，期望 110", totals.InputTokens)
	}
	if totals.OutputTokens == nil || *totals.OutputTokens != 220 {
		t.Fatalf("totals.output_tokens = %v，期望 220", totals.OutputTokens)
	}
	if totals.TotalTokens == nil || *totals.TotalTokens != 330 {
		t.Fatalf("totals.total_tokens = %v，期望 330", totals.TotalTokens)
	}
	if totals.P95LatencyMS == nil || *totals.P95LatencyMS != 190 {
		t.Fatalf("totals.p95_latency_ms = %v，期望 190", totals.P95LatencyMS)
	}

	if len(stats.Hourly) != 1 {
		t.Fatalf("hourly 桶数 = %d，期望 1", len(stats.Hourly))
	}
	hourly := stats.Hourly[0]
	if !hourly.Hour.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("hourly 桶起点 = %v", hourly.Hour)
	}
	if hourly.Metrics.RequestCount != 20 ||
		hourly.Metrics.P95LatencyMS == nil || *hourly.Metrics.P95LatencyMS != 190 {
		t.Fatalf("hourly metrics = %+v", hourly.Metrics)
	}

	if len(stats.ByModel) != 2 || len(stats.ByChannel) != 2 {
		t.Fatalf("分组行数 = %d/%d", len(stats.ByModel), len(stats.ByChannel))
	}

	modelStats := make(map[string]StatsMetrics, len(stats.ByModel))
	for _, row := range stats.ByModel {
		modelStats[row.Model] = row.Metrics
	}
	gpt5 := modelStats["gpt-5"]
	if gpt5.RequestCount != 10 || gpt5.SuccessCount != 8 || gpt5.ErrorCount != 2 ||
		gpt5.UsageCount != 5 {
		t.Fatalf("gpt-5 计数 = %+v", gpt5)
	}
	assertFloatValue(t, "gpt-5.avg", gpt5.AvgLatencyMS, 55)
	if gpt5.P95LatencyMS == nil || *gpt5.P95LatencyMS != 100 {
		t.Fatalf("gpt-5.p95 = %v，期望 100", gpt5.P95LatencyMS)
	}
	if gpt5.InputTokens == nil || *gpt5.InputTokens != 30 {
		t.Fatalf("gpt-5.input_tokens = %v，期望 30", gpt5.InputTokens)
	}

	gpt4 := modelStats["gpt-4"]
	if gpt4.RequestCount != 10 || gpt4.SuccessCount != 8 || gpt4.ErrorCount != 2 {
		t.Fatalf("gpt-4 计数 = %+v", gpt4)
	}
	assertFloatValue(t, "gpt-4.avg", gpt4.AvgLatencyMS, 155)
	if gpt4.P95LatencyMS == nil || *gpt4.P95LatencyMS != 200 {
		t.Fatalf("gpt-4.p95 = %v，期望 200", gpt4.P95LatencyMS)
	}

	channelStats := make(map[string]ChannelStats, len(stats.ByChannel))
	for _, row := range stats.ByChannel {
		channelStats[row.ChannelName] = row
	}
	primary := channelStats["primary"]
	if primary.ChannelID == nil || *primary.ChannelID != channelA ||
		primary.Metrics.RequestCount != 10 || primary.Metrics.P95LatencyMS == nil ||
		*primary.Metrics.P95LatencyMS != 100 {
		t.Fatalf("primary 聚合 = %+v", primary)
	}
	secondary := channelStats["secondary"]
	if secondary.ChannelID == nil || *secondary.ChannelID != channelB ||
		secondary.Metrics.RequestCount != 10 || secondary.Metrics.P95LatencyMS == nil ||
		*secondary.Metrics.P95LatencyMS != 200 {
		t.Fatalf("secondary 聚合 = %+v", secondary)
	}

	filtered, err := obs.Stats(ctx, StatsFilter{
		From:  testBase.Add(-time.Hour),
		To:    testBase.Add(2 * time.Hour),
		Model: "gpt-4",
	})
	if err != nil {
		t.Fatalf("按模型 Stats: %v", err)
	}
	if filtered.Totals.RequestCount != 10 ||
		len(filtered.ByChannel) != 1 ||
		filtered.ByChannel[0].ChannelName != "secondary" {
		t.Fatalf("按模型过滤结果 = %+v", filtered)
	}

	byChannelFiltered, err := obs.Stats(ctx, StatsFilter{
		From:      testBase.Add(-time.Hour),
		To:        testBase.Add(2 * time.Hour),
		ChannelID: &channelA,
	})
	if err != nil {
		t.Fatalf("按渠道 Stats: %v", err)
	}
	if byChannelFiltered.Totals.RequestCount != 10 ||
		len(byChannelFiltered.ByModel) != 1 ||
		byChannelFiltered.ByModel[0].Model != "gpt-5" {
		t.Fatalf("按渠道过滤结果 = %+v", byChannelFiltered)
	}
}

func TestWriteRedactsSecretsInErrorMessages(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "redaction")
	ctx := context.Background()

	log := testRequestLog("req-redacted", channelID, "gpt-5", testBase)
	log.ErrorMessage = "upstream failed: Authorization: Bearer sk-live-0123456789abcdef"
	if err := obs.WriteRequest(ctx, log); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if err := obs.WriteAttempt(ctx, RequestAttempt{
		RequestID:    "req-redacted",
		AttemptIndex: 1,
		ChannelID:    &channelID,
		Status:       StatusError,
		ErrorMessage: "api_key=sk-live-0123456789abcdef",
		StartedAt:    testBase,
	}); err != nil {
		t.Fatalf("WriteAttempt: %v", err)
	}

	detail, err := obs.GetRequest(ctx, "req-redacted")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	for _, message := range []string{
		detail.Request.ErrorMessage,
		detail.Attempts[0].ErrorMessage,
	} {
		if strings.Contains(message, "sk-live-0123456789abcdef") {
			t.Fatalf("错误详情未脱敏：%q", message)
		}
	}
}

func TestStatsEmptyRange(t *testing.T) {
	obs, _ := newTestObservability(t)
	stats, err := obs.Stats(context.Background(), StatsFilter{
		From: testBase.Add(24 * time.Hour),
		To:   testBase.Add(25 * time.Hour),
	})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Totals.RequestCount != 0 || stats.Totals.SuccessCount != 0 ||
		stats.Totals.ErrorCount != 0 || stats.Totals.UsageCount != 0 {
		t.Fatalf("空范围 totals = %+v", stats.Totals)
	}
	if stats.Totals.ErrorRate != nil || stats.Totals.AvgLatencyMS != nil ||
		stats.Totals.P95LatencyMS != nil ||
		stats.Totals.InputTokens != nil || stats.Totals.OutputTokens != nil ||
		stats.Totals.TotalTokens != nil {
		t.Fatalf("空范围应返回 null 指标：%+v", stats.Totals)
	}
	if len(stats.Hourly) != 0 || len(stats.ByModel) != 0 || len(stats.ByChannel) != 0 {
		t.Fatalf("空范围分组 = %d/%d/%d",
			len(stats.Hourly), len(stats.ByModel), len(stats.ByChannel))
	}
}

func TestStatsKeepsDeletedChannelSnapshot(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "to-delete")
	ctx := context.Background()

	if err := obs.WriteRequest(ctx,
		testRequestLog("req-deleted-channel", channelID, "gpt-5", testBase)); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	if _, err := st.DB().Exec(`DELETE FROM channels WHERE id = ?`, channelID); err != nil {
		t.Fatalf("删除渠道: %v", err)
	}

	detail, err := obs.GetRequest(ctx, "req-deleted-channel")
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if detail.Request.ChannelID != nil {
		t.Fatalf("删除渠道后 channel_id = %v，期望 NULL", detail.Request.ChannelID)
	}
	if detail.Request.ChannelName != "channel-"+fmt.Sprint(channelID) {
		t.Fatalf("删除渠道后 channel_name = %q，期望保留快照",
			detail.Request.ChannelName)
	}

	stats, err := obs.Stats(ctx, StatsFilter{
		From: testBase.Add(-time.Hour),
		To:   testBase.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if len(stats.ByChannel) != 1 {
		t.Fatalf("删除渠道后 by_channel 行数 = %d，期望 1", len(stats.ByChannel))
	}
	row := stats.ByChannel[0]
	if row.ChannelID != nil || row.ChannelName != detail.Request.ChannelName {
		t.Fatalf("删除渠道后聚合维度 = %v/%q，期望 NULL/快照",
			row.ChannelID, row.ChannelName)
	}
	if row.Metrics.RequestCount != 1 ||
		row.Metrics.P95LatencyMS == nil || *row.Metrics.P95LatencyMS != 100 {
		t.Fatalf("删除渠道后聚合指标 = %+v", row.Metrics)
	}
}

func TestObservabilityIndexesExist(t *testing.T) {
	_, st := newTestObservability(t)
	for _, index := range []string{
		"idx_request_logs_started_at",
		"idx_request_logs_channel_id",
		"idx_request_logs_model",
		"idx_request_logs_status",
		"idx_request_attempts_request_id",
		"idx_request_attempts_channel_id",
	} {
		var count int
		if err := st.DB().QueryRow(
			`SELECT count(*) FROM sqlite_master WHERE type = 'index' AND name = ?`,
			index,
		).Scan(&count); err != nil {
			t.Fatalf("查询索引 %s: %v", index, err)
		}
		if count != 1 {
			t.Fatalf("索引 %s 不存在", index)
		}
	}
}

func TestPruneRequestLogsLeavesCaptures(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "retention")
	ctx := context.Background()
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		startedAt := now.Add(-100 * 24 * time.Hour)
		requestID := fmt.Sprintf("old-%d", i)
		log := testRequestLog(requestID, channelID, "gpt-5", startedAt)
		log.FinishedAt = testTimePtr(startedAt.Add(time.Second))
		if err := obs.WriteRequest(ctx, log); err != nil {
			t.Fatalf("写入旧日志 %s: %v", requestID, err)
		}
		if err := obs.WriteAttempt(ctx, RequestAttempt{
			RequestID:    requestID,
			AttemptIndex: 1,
			ChannelID:    &channelID,
			ChannelName:  "retention",
			Status:       StatusSuccess,
			StartedAt:    startedAt,
			FinishedAt:   testTimePtr(startedAt.Add(time.Second)),
		}); err != nil {
			t.Fatalf("写入旧 attempt %s: %v", requestID, err)
		}
	}

	newStarted := now.Add(-24 * time.Hour)
	if err := obs.WriteRequest(ctx,
		testRequestLog("recent-1", channelID, "gpt-5", newStarted)); err != nil {
		t.Fatalf("写入新日志: %v", err)
	}

	if _, err := st.DB().Exec(`
		INSERT INTO request_captures (request_id, created_at, expires_at)
		VALUES (?, ?, ?)`,
		"old-0", formatTime(now), formatTime(now.Add(24*time.Hour))); err != nil {
		t.Fatalf("写入捕获行: %v", err)
	}

	result, err := obs.PruneRequestLogs(ctx, now.Add(-DefaultRetention), 2)
	if err != nil {
		t.Fatalf("PruneRequestLogs: %v", err)
	}
	if result.Requests != 5 || result.Attempts != 5 {
		t.Fatalf("清理结果 = %+v，期望 5/5", result)
	}

	var logs, attempts, captures int
	if err := st.DB().QueryRow(`SELECT count(*) FROM request_logs`).Scan(&logs); err != nil {
		t.Fatalf("统计日志: %v", err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM request_attempts`).Scan(&attempts); err != nil {
		t.Fatalf("统计 attempts: %v", err)
	}
	if err := st.DB().QueryRow(`SELECT count(*) FROM request_captures`).Scan(&captures); err != nil {
		t.Fatalf("统计 captures: %v", err)
	}
	if logs != 1 || attempts != 0 {
		t.Fatalf("清理后 logs/attempts = %d/%d，期望 1/0", logs, attempts)
	}
	if captures != 1 {
		t.Fatalf("清理日志后 captures = %d，期望保留 1", captures)
	}

	empty, err := obs.PruneRequestLogs(ctx, now.Add(-DefaultRetention), 2)
	if err != nil {
		t.Fatalf("再次清理: %v", err)
	}
	if empty.Requests != 0 || empty.Attempts != 0 {
		t.Fatalf("再次清理结果 = %+v，期望 0/0", empty)
	}
}

func TestWriteRejectsInvalidRecords(t *testing.T) {
	obs, st := newTestObservability(t)
	channelID := seedObservabilityChannel(t, st, "validation")
	ctx := context.Background()

	invalidID := testRequestLog("", channelID, "gpt-5", testBase)
	if err := obs.WriteRequest(ctx, invalidID); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("空 request_id err = %v，期望 ErrInvalidRecord", err)
	}

	negativeTokens := testRequestLog("req-negative", channelID, "gpt-5", testBase)
	negativeTokens.InputTokens = testInt64Ptr(-1)
	if err := obs.WriteRequest(ctx, negativeTokens); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("负 token err = %v，期望 ErrInvalidRecord", err)
	}

	if err := obs.WriteAttempt(ctx, RequestAttempt{
		RequestID:    "req-attempt",
		AttemptIndex: 0,
		Status:       StatusError,
		StartedAt:    testBase,
	}); !errors.Is(err, ErrInvalidRecord) {
		t.Fatalf("attempt_index=0 err = %v，期望 ErrInvalidRecord", err)
	}
}

func assertFloatValue(t *testing.T, name string, got *float64, want float64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil，期望 %v", name, want)
	}
	if math.Abs(*got-want) > 1e-9 {
		t.Fatalf("%s = %v，期望 %v", name, *got, want)
	}
}
