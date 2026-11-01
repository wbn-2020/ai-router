package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ai-router/internal/observability"
	"ai-router/internal/routing"
)

// strictSSEData is deliberately independent of upstream.SSEReader: only a blank
// line dispatches an event, and event-only blocks and EOF tails do not dispatch.
func strictSSEData(body string) []string {
	scanner := bufio.NewScanner(strings.NewReader(body))
	var events []string
	var data []string
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			if len(data) > 0 {
				events = append(events, strings.Join(data, "\n"))
			}
			data = nil
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	return events
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines   []time.Time
	deadlineErr error
	flushErr    error
}

func (w *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return w.deadlineErr
}
func (w *deadlineRecorder) FlushError() error {
	if w.flushErr != nil {
		return w.flushErr
	}
	w.ResponseRecorder.Flush()
	return nil
}

func TestResponsesTerminalRequiresDispatchableValidPayload(t *testing.T) {
	for _, frame := range []string{
		"event: response.completed\n\n",
		"event: response.completed\ndata: {}\n\n",
		"event: response.completed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\"}}\n\n",
		strings.TrimRight(responsesCompletedFrame(1, "m"), "\n"),
	} {
		t.Run(frame, func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			enableProxyStream(t, stack)
			first := newSSEFake(t, frame)
			second := newSSEFake(t, responsesCompletedFrame(1, "good"))
			addResponsesChannel(t, stack, "bad", first)
			addResponsesChannel(t, stack, "good", second)
			rec := stack.do(t, PathResponses, streamResponsesBody())
			if second.hits.Load() != 1 {
				t.Fatalf("invalid terminal did not fail over: %s", rec.Body.String())
			}
			events := strictSSEData(rec.Body.String())
			if len(events) != 1 || !strings.Contains(events[0], `"model":"good"`) {
				t.Fatalf("dispatchable events = %v", events)
			}
		})
	}
}

func TestResponsesEmptyCompletedAndIncomplete(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "incomplete"}[incomplete], func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			enableProxyStream(t, stack)
			frame := responsesCompletedFrame(1, "m")
			if incomplete {
				frame = strings.ReplaceAll(frame, "completed", "incomplete")
				frame = strings.Replace(frame, `"status":"incomplete"`, `"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"}`, 1)
			}
			fake := newSSEFake(t, frame)
			channel := addResponsesChannel(t, stack, "one", fake)
			rec := stack.do(t, PathResponses, streamResponsesBody())
			if rec.Code != 200 || len(strictSSEData(rec.Body.String())) != 1 {
				t.Fatalf("terminal response = %d %s", rec.Code, rec.Body.String())
			}
			if stack.scheduler.Circuit(channel.ID).Open {
				t.Fatal("valid terminal penalized channel")
			}
			log := stack.latestDetail(t).Request
			if incomplete && log.ErrorCode != "upstream_incomplete" {
				t.Fatalf("incomplete recorded as completed: %+v", log)
			}
			if !incomplete && log.Status != observability.StatusSuccess {
				t.Fatalf("empty completed rejected: %+v", log)
			}
		})
	}
}

func TestStreamFirstContentBudgetNotRefreshedByHeartbeat(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stream := enableProxyStream(t, stack)
	stream.firstContentTimeout = 60 * time.Millisecond
	stream.requestTimeout = time.Second
	fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				_, _ = io.WriteString(w, ": heartbeat\n\n")
				w.(http.Flusher).Flush()
			}
		}
	})
	channel := addResponsesChannel(t, stack, "heartbeat", fake)
	start := time.Now()
	rec := stack.do(t, PathResponses, streamResponsesBody())
	if rec.Code != 504 || time.Since(start) > time.Second {
		t.Fatalf("first content budget: %d %s", rec.Code, rec.Body.String())
	}
	if stack.scheduler.Circuit(channel.ID).Open {
		t.Fatal("gateway budget penalized upstream")
	}
	if stack.latestDetail(t).Request.Status == observability.StatusCancelled {
		t.Fatal("budget reported as cancellation")
	}
}

func TestProxyTotalBudgetIncludesRetries(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stack.proxy.requestTimeout = 80 * time.Millisecond
	for _, name := range []string{"a", "b", "c"} {
		fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-time.After(50 * time.Millisecond):
				w.WriteHeader(503)
			case <-r.Context().Done():
			}
		})
		addResponsesChannel(t, stack, name, fake)
	}
	start := time.Now()
	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != 504 || time.Since(start) > 250*time.Millisecond {
		t.Fatalf("total budget: %d elapsed=%v body=%s", rec.Code, time.Since(start), rec.Body.String())
	}
	if stack.latestDetail(t).Request.Status == observability.StatusCancelled {
		t.Fatal("total budget reported as 499")
	}
}

func TestResponseControllerErrorsAreNotSilenced(t *testing.T) {
	for _, flush := range []bool{false, true} {
		stack := newProxyTestStack(t, proxyStackOptions{})
		enableProxyStream(t, stack)
		fake := newSSEFake(t, responsesCompletedFrame(1, "m"))
		addResponsesChannel(t, stack, "one", fake)
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		if flush {
			w.flushErr = errors.New("flush failed")
		} else {
			w.deadlineErr = http.ErrNotSupported
		}
		req := httptest.NewRequest("POST", PathResponses, strings.NewReader(streamResponsesBody()))
		req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
		stack.handler.ServeHTTP(w, req)
		log := stack.latestDetail(t).Request
		if log.Status == observability.StatusSuccess || log.HTTPStatus == nil || *log.HTTPStatus == 499 {
			t.Fatalf("write failure lost or misclassified: %+v", log)
		}
	}
}

func TestBoundedStructuredErrorScopes(t *testing.T) {
	for _, tc := range []struct {
		code, scope string
		retry       bool
	}{
		{"context_length_exceeded", routing.FailureScopeRequest, false},
		{"invalid_request_error", routing.FailureScopeRequest, false},
		{"model_not_found", routing.FailureScopeModel, true},
		{"invalid_api_key", routing.FailureScopeChannel, true},
		{"server_error", routing.FailureScopeChannel, true},
		{"unknown_vendor_error", routing.FailureScopeChannel, true},
	} {
		payload := []byte(`{"error":{"code":"` + tc.code + `","message":"secret not copied"}}`)
		f := classifyStructuredFailure(classifyUpstreamStatus(500), payload)
		if f.Scope != tc.scope || f.Retry != tc.retry || strings.Contains(f.Message, "secret") {
			t.Fatalf("%s = %+v", tc.code, f)
		}
	}
	if parseRetryAfter("999999999999999999999999", time.Now()) > maxRetryAfter {
		t.Fatal("retry-after overflow")
	}
}

func TestDownstreamDeadlineNeverExceedsTotalBudget(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	if err := renewWriteDeadline(ctx, w, time.Hour); err != nil {
		t.Fatal(err)
	}
	deadline, _ := ctx.Deadline()
	if len(w.deadlines) != 1 || w.deadlines[0].After(deadline) {
		t.Fatalf("deadlines = %v total=%v", w.deadlines, deadline)
	}
}

func TestStreamFirstContentTimerStopsAfterCommitButTotalRemains(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stream := enableProxyStream(t, stack)
	stream.firstContentTimeout = 40 * time.Millisecond
	stream.requestTimeout = 140 * time.Millisecond
	fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesTextDeltaFrame(1, "hello"))
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	})
	channel := addResponsesChannel(t, stack, "long", fake)
	start := time.Now()
	rec := stack.do(t, PathResponses, streamResponsesBody())
	elapsed := time.Since(start)
	log := stack.latestDetail(t).Request
	if rec.Code != 200 || elapsed < 100*time.Millisecond || elapsed > time.Second || log.ErrorCode != "gateway_request_timeout" {
		t.Fatalf("post-commit timeout elapsed=%v response=%s log=%+v", elapsed, rec.Body.String(), log)
	}
	if stack.scheduler.Circuit(channel.ID).Open {
		t.Fatal("total deadline penalized channel")
	}
}

func TestStructuredRequestErrorDoesNotRetryHTTPOrSSE(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "SSE"}[stream], func(t *testing.T) {
			stack := newProxyTestStack(t, proxyStackOptions{})
			enableProxyStream(t, stack)
			var first *proxyFakeUpstream
			payload := `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"private prompt"}}`
			body := proxyRequestBody()
			if stream {
				body = streamResponsesBody()
				first = newSSEFake(t, `event: error
data: `+payload+`

`)
			} else {
				first = newProxyJSONUpstream(t, 400, payload)
			}
			second := newProxyJSONUpstream(t, 200, `{"ok":true}`)
			channel := addResponsesChannel(t, stack, "bad-request", first)
			addResponsesChannel(t, stack, "spare", second)
			rec := stack.do(t, PathResponses, body)
			if rec.Code != 400 || second.hits.Load() != 0 || strings.Contains(rec.Body.String(), "private prompt") {
				t.Fatalf("request classification: %d %s retries=%d", rec.Code, rec.Body.String(), second.hits.Load())
			}
			if stack.scheduler.Circuit(channel.ID).Open {
				t.Fatal("request error isolated channel")
			}
		})
	}
}

func TestCaptureWriterPropagatesFlushAndDeadline(t *testing.T) {
	want := errors.New("flush failed")
	base := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), flushErr: want}
	writer := &captureResponseWriter{ResponseWriter: base}
	if err := http.NewResponseController(writer).Flush(); !errors.Is(err, want) {
		t.Fatalf("flush error = %v", err)
	}
	if err := renewWriteDeadline(context.Background(), writer, time.Second); err != nil || len(base.deadlines) != 1 {
		t.Fatalf("deadline propagation: %v %v", err, base.deadlines)
	}
}

func TestEarlierInboundDeadlineWins(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stack.proxy.requestTimeout = time.Second
	fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	channel := addResponsesChannel(t, stack, "slow", fake)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest("POST", PathResponses, strings.NewReader(proxyRequestBody())).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	start := time.Now()
	stack.handler.ServeHTTP(w, req)
	if w.Code != 504 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("inbound deadline: %d %v", w.Code, time.Since(start))
	}
	if stack.scheduler.Circuit(channel.ID).Open {
		t.Fatal("inbound deadline penalized channel")
	}
}

func TestDownstreamTimeoutIsNotCancellation(t *testing.T) {
	failure := downstreamFailure(context.Background(), context.DeadlineExceeded)
	if failure.Status != 504 || failure.Canceled || failure.Retry || failure.Scope != routing.FailureScopeRequest {
		t.Fatalf("write timeout = %+v", failure)
	}
}

func TestHTTPModelErrorIsolatesOnlyModel(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	first := newProxyJSONUpstream(t, 403, `{"error":{"code":"model_access_denied","message":"private"}}`)
	second := newProxyJSONUpstream(t, 200, `{"ok":true}`)
	channel := addResponsesChannel(t, stack, "model-denied", first)
	addResponsesChannel(t, stack, "fallback", second)
	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != 200 || second.hits.Load() != 1 {
		t.Fatalf("model failover = %d %s", rec.Code, rec.Body.String())
	}
	if stack.scheduler.Circuit(channel.ID).Open {
		t.Fatal("model access error isolated entire channel")
	}
}

func TestProxyBudgetIncludesCapacityWait(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{CapacityWait: time.Second})
	stack.proxy.requestTimeout = 40 * time.Millisecond
	fake := newProxyJSONUpstream(t, 200, `{"ok":true}`)
	addResponsesChannel(t, stack, "busy", fake)
	_, slot, err := stack.scheduler.Acquire(context.Background(), proxyTestModel, ProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	start := time.Now()
	rec := stack.do(t, PathResponses, proxyRequestBody())
	if rec.Code != 504 || fake.hits.Load() != 0 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("capacity budget: status=%d hits=%d elapsed=%v", rec.Code, fake.hits.Load(), time.Since(start))
	}
}

func TestErrorParsingBoundsAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Duration
	}{
		{"12", 12 * time.Second}, {"999999", maxRetryAfter}, {"-1", 0}, {"nonsense", 0},
		{now.Add(time.Minute).Format(http.TimeFormat), time.Minute},
	} {
		if got := parseRetryAfter(tc.raw, now); got != tc.want {
			t.Fatalf("Retry-After %q = %v want %v", tc.raw, got, tc.want)
		}
	}
	for _, body := range []string{
		`{"error":{"code":"` + strings.Repeat("x", maxErrorFieldBytes+1) + `","type":"invalid_request_error"}}`,
		`{"error":{"code":"invalid_request_error","message":"` + strings.Repeat("x", maxErrorBodyBytes) + `"}}`,
	} {
		failure := classifyStructuredFailure(classifyUpstreamStatus(500), []byte(body))
		if failure.Scope != routing.FailureScopeChannel || !failure.Retry {
			t.Fatalf("oversized error influenced classification: %+v", failure)
		}
	}
}

func TestWriteTimeoutWithCanceledTransportRemainsTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	failure := downstreamFailure(ctx, context.DeadlineExceeded)
	if failure.Status != 504 || failure.Canceled {
		t.Fatalf("write timeout masked by transport cancellation: %+v", failure)
	}
}
