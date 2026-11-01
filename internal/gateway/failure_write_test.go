package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFailureDeliveryHasBoundedWriteWindow(t *testing.T) {
	for _, budget := range []time.Duration{25 * time.Millisecond, 30 * time.Second} {
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		before := time.Now()
		writeFailure(w, proxyFailure{Status: http.StatusGatewayTimeout, Code: "gateway_request_timeout", Message: "Request deadline exceeded."}, budget)
		if len(w.deadlines) != 1 || w.deadlines[0].Before(before) || w.deadlines[0].After(time.Now().Add(min(budget, time.Second))) {
			t.Fatalf("error delivery deadline = %v", w.deadlines)
		}
		if w.Code != http.StatusGatewayTimeout || !w.Flushed {
			t.Fatalf("error response not delivered: %d %s", w.Code, w.Body.String())
		}
	}
}

func TestFailureDeliveryStopsOnDeadlineFailureOrCancellation(t *testing.T) {
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder(), deadlineErr: http.ErrNotSupported}
	writeFailure(w, proxyFailure{Status: http.StatusGatewayTimeout}, time.Second)
	if w.Body.Len() != 0 || w.Flushed {
		t.Fatal("deadline failure was ignored")
	}
	w = &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	writeFailure(w, classifyUpstreamError(context.Canceled), time.Second)
	if len(w.deadlines) != 0 || w.Body.Len() != 0 {
		t.Fatal("canceled client received an error write")
	}
}
