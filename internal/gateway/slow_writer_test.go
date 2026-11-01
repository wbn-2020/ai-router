package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

type blockingResponseWriter struct {
	*deadlineRecorder
}

func (w *blockingResponseWriter) Write(_ []byte) (int, error) {
	deadline := w.deadlines[len(w.deadlines)-1]
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
	return 0, os.ErrDeadlineExceeded
}

func TestSlowStreamWriterReleasesSlotAndCancelsUpstream(t *testing.T) {
	stack := newProxyTestStack(t, proxyStackOptions{})
	stream := enableProxyStream(t, stack)
	stream.writeTimeout = 30 * time.Millisecond
	stream.requestTimeout = time.Second
	canceled := make(chan struct{})
	fake := newProxyFakeUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, responsesTextDeltaFrame(1, "hello"))
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
			close(canceled)
		case <-time.After(2 * time.Second):
			t.Error("上游未收到取消")
		}
	})
	channel := addResponsesChannel(t, stack, "slow-reader", fake)
	w := &blockingResponseWriter{&deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}}
	req := httptest.NewRequest(http.MethodPost, PathResponses, strings.NewReader(streamResponsesBody()))
	req.Header.Set("Authorization", "Bearer "+proxyTestClientKey)
	start := time.Now()
	stack.handler.ServeHTTP(w, req)
	if time.Since(start) > time.Second {
		t.Fatal("慢客户端未在写预算内退出")
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("未关闭上游请求")
	}
	if runtime := stack.scheduler.Runtime(channel); runtime.InUse != 0 || runtime.State != "ready" {
		t.Fatalf("慢客户端误罚上游或泄漏槽位：%+v", runtime)
	}
	detail := stack.latestDetail(t)
	if len(detail.Attempts) != 1 || detail.Request.ErrorCode != "downstream_write_timeout" {
		t.Fatalf("慢写错误未正确收尾：%+v", detail)
	}
}
