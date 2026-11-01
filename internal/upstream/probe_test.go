package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"ai-router/internal/store"
)

const validResponsesProbe = `{"object":"response","status":"completed","error":null,"output":[{"type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong"}]}]}`
const validChatProbe = `{"object":"chat.completion","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"pong"}}]}`

func TestProbeRecordsCompleteDuration(t *testing.T) {
	server, requests := newCaptureServer(t, http.StatusOK, validResponsesProbe)
	recorder := &fakeHealthRecorder{}
	prober, err := NewProber(newTestClient(t, Options{}), recorder)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Unix(1_000, 0)
	times := []time.Time{start, start.Add(7 * time.Millisecond), start.Add(43 * time.Millisecond), start.Add(44 * time.Millisecond)}
	prober.now = func() time.Time {
		at := times[0]
		times = times[1:]
		return at
	}
	result, err := prober.Probe(context.Background(), testTarget(server, store.ChannelProtocolOpenAIResponses), "gpt-upstream")
	if err != nil {
		t.Fatal(err)
	}
	receiveCaptured(t, requests)
	if result.Latency != 7*time.Millisecond || result.LatencyMS != 7 || result.DurationMS != 43 {
		t.Fatalf("timing = %+v, want headers 7ms and duration 43ms", result)
	}
	calls := recorder.snapshot(t)
	if len(calls) != 1 || calls[0].Health.DurationMS == nil || *calls[0].Health.DurationMS != 43 {
		t.Fatalf("recorded duration = %+v", calls)
	}
}

func TestProbeValidatesGeneration(t *testing.T) {
	tests := []struct {
		name, protocol, body, status string
	}{
		{"HTML 200", store.ChannelProtocolOpenAIResponses, `<html>OK</html>`, "unhealthy"},
		{"empty object", store.ChannelProtocolOpenAIResponses, `{}`, "unhealthy"},
		{"old success fixture", store.ChannelProtocolOpenAIResponses, `{"ok":true}`, "unhealthy"},
		{"JSON array", store.ChannelProtocolOpenAIResponses, `[]`, "unhealthy"},
		{"trailing JSON", store.ChannelProtocolOpenAIResponses, validResponsesProbe + `{}`, "unhealthy"},
		{"200 error", store.ChannelProtocolOpenAIResponses, `{"error":{"message":"sk-private-probe-secret"}}`, "unhealthy"},
		{"completed with error", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"error":null`, `"error":{"message":"sk-private-probe-secret"}`, 1), "unhealthy"},
		{"missing terminal", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"status":"completed",`, "", 1), "unhealthy"},
		{"in progress", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"status":"completed"`, `"status":"in_progress"`, 1), "unhealthy"},
		{"empty completed", store.ChannelProtocolOpenAIResponses, `{"object":"response","status":"completed","output":[]}`, "unhealthy"},
		{"reasoning only", store.ChannelProtocolOpenAIResponses, `{"object":"response","status":"completed","output":[{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}]}`, "unhealthy"},
		{"blank text", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"pong"`, `"  \n "`, 1), "unhealthy"},
		{"wrong role", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"assistant"`, `"user"`, 1), "unhealthy"},
		{"Responses budget exhausted", store.ChannelProtocolOpenAIResponses, `{"object":"response","status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[]}`, "inconclusive"},
		{"Responses incomplete partial", store.ChannelProtocolOpenAIResponses, strings.Replace(validResponsesProbe, `"status":"completed"`, `"status":"incomplete"`, 1), "inconclusive"},
		{"Responses valid", store.ChannelProtocolOpenAIResponses, validResponsesProbe, "healthy"},
		{"Responses tool", store.ChannelProtocolOpenAIResponses, `{"object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"q\":\"ping\"}"}]}`, "healthy"},
		{"Responses malformed tool", store.ChannelProtocolOpenAIResponses, `{"object":"response","status":"completed","output":[{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{"}]}`, "unhealthy"},
		{"Chat generic success", store.ChannelProtocolOpenAIChat, `{"ok":true}`, "unhealthy"},
		{"Chat wrong envelope", store.ChannelProtocolOpenAIChat, validResponsesProbe, "unhealthy"},
		{"Responses wrong envelope", store.ChannelProtocolOpenAIResponses, validChatProbe, "unhealthy"},
		{"Chat no choices", store.ChannelProtocolOpenAIChat, `{"object":"chat.completion","choices":[]}`, "unhealthy"},
		{"Chat missing finish", store.ChannelProtocolOpenAIChat, strings.Replace(validChatProbe, `"stop"`, `null`, 1), "unhealthy"},
		{"Chat blank", store.ChannelProtocolOpenAIChat, strings.Replace(validChatProbe, `"pong"`, `" "`, 1), "unhealthy"},
		{"Chat budget exhausted", store.ChannelProtocolOpenAIChat, strings.Replace(validChatProbe, `"stop"`, `"length"`, 1), "inconclusive"},
		{"Chat budget before output", store.ChannelProtocolOpenAIChat, `{"object":"chat.completion","choices":[{"finish_reason":"length","message":{"role":"assistant","content":null}}]}`, "inconclusive"},
		{"Chat filtered", store.ChannelProtocolOpenAIChat, strings.Replace(validChatProbe, `"stop"`, `"content_filter"`, 1), "unhealthy"},
		{"Chat valid", store.ChannelProtocolOpenAIChat, validChatProbe, "healthy"},
		{"Chat tool", store.ChannelProtocolOpenAIChat, `{"object":"chat.completion","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`, "healthy"},
		{"Chat malformed tool", store.ChannelProtocolOpenAIChat, `{"object":"chat.completion","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"","arguments":"{}"}}]}}]}`, "unhealthy"},
		{"oversize", store.ChannelProtocolOpenAIResponses, validResponsesProbe + strings.Repeat(" ", probeDrainLimit), "unhealthy"},
		{"exact size", store.ChannelProtocolOpenAIResponses, validResponsesProbe + strings.Repeat(" ", probeDrainLimit-len(validResponsesProbe)), "healthy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server, requests := newCaptureServer(t, http.StatusOK, tt.body)
			prober, err := NewProber(newTestClient(t, Options{}), nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := prober.Probe(context.Background(), testTarget(server, tt.protocol), "gpt-upstream")
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tt.status || result.Healthy() != (tt.status == "healthy") {
				t.Errorf("Probe() = %+v, want %s", result, tt.status)
			}
			if (result.ErrorCode == "") != (tt.status == HealthStatusHealthy) {
				t.Errorf("ErrorCode = %q, status = %s", result.ErrorCode, tt.status)
			}
			if tt.status == HealthStatusInconclusive {
				wantCode := "response_incomplete"
				if tt.protocol == store.ChannelProtocolOpenAIChat {
					wantCode = "output_limit"
				}
				if result.ErrorCode != wantCode {
					t.Errorf("ErrorCode = %q, want %q", result.ErrorCode, wantCode)
				}
			}
			if strings.Contains(result.Error, "sk-private-probe-secret") {
				t.Errorf("probe leaked error body: %s", result.Error)
			}
			request := receiveCaptured(t, requests)
			payload := decodeJSONObject(t, request.Body)
			budgetKey := "max_output_tokens"
			if tt.protocol == store.ChannelProtocolOpenAIChat {
				budgetKey = "max_tokens"
			}
			if payload[budgetKey] != json.Number("16") || payload["stream"] != false {
				t.Errorf("probe request = %s, want non-stream with 16-token budget", request.Body)
			}
			select {
			case <-requests:
				t.Fatal("probe unexpectedly retried")
			default:
			}
		})
	}
}
