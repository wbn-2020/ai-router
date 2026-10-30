package gateway

import (
	"net/http"
	"strings"
	"testing"

	"ai-router/internal/observability"
	storepkg "ai-router/internal/store"
)

// TestObserveGatewayCapturesBodies 验证诊断环在真实数据面 Handler 外围
// 同时保存请求与响应字节，且使用的 request_id 与 request_logs 一致。
func TestObserveGatewayCapturesBodies(t *testing.T) {
	const requestID = "req-capture-wiring-test"
	observability.DefaultCaptureRing().Delete(requestID)
	t.Cleanup(func() { observability.DefaultCaptureRing().Delete(requestID) })

	stack := newProxyTestStack(t, proxyStackOptions{})
	upstream := newProxyJSONUpstream(t, http.StatusOK, `{"id":"resp_capture","output":[]}`)
	channel := stack.addChannel(t, "capture-upstream", upstream.server.URL+"/v1",
		storepkg.ChannelProtocolOpenAIResponses, 1)
	stack.addRoute(t, channel.ID, "capture-upstream-model")
	stack.proxy.newRequestID = func(*http.Request) string { return requestID }

	observed, err := ObserveGateway(stack.handler, stack.proxy, nil, stack.obs)
	if err != nil {
		t.Fatalf("ObserveGateway: %v", err)
	}

	body := proxyRequestBody()
	rec := doObserved(t, observed, PathResponses, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("响应 = %d（body=%s）", rec.Code, rec.Body.String())
	}

	snapshot, ok := observability.DefaultCaptureRing().Get(requestID)
	if !ok {
		t.Fatal("诊断环没有记录请求")
	}
	if string(snapshot.Request.Data) != body {
		t.Errorf("请求捕获 = %q，期望 %q", snapshot.Request.Data, body)
	}
	if !strings.Contains(string(snapshot.Response.Data), `"resp_capture"`) {
		t.Errorf("响应捕获缺少上游响应：%q", snapshot.Response.Data)
	}
	if snapshot.Request.Truncated || snapshot.Response.Truncated {
		t.Errorf("小 body 不应标记 truncated：%+v", snapshot)
	}
}
