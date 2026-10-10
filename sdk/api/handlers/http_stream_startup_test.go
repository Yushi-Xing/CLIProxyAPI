package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func testHTTPStartup(t *testing.T, protocol string, sse bool) (*HTTPStreamStartup, *httptest.ResponseRecorder) {
	t.Helper()
	r := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(r)
	c.Request = httptest.NewRequest(http.MethodPost, "/stream", nil)
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	_, s, _ := h.PrepareHTTPStream(c, context.Background(), func(...interface{}) {}, protocol, sse)
	t.Cleanup(s.Close)
	return s, r
}

func TestHTTPStreamStartupDefaultsAndOptIn(t *testing.T) {
	for _, timeout := range []int{-1, 0, 17} {
		for _, enabled := range []bool{false, true} {
			for _, sse := range []bool{false, true} {
				s, _ := testHTTPStartup(t, "openai", sse)
				s.Close()
				cfg := &sdkconfig.SDKConfig{}
				cfg.Streaming.FirstChunkTimeoutSeconds = timeout
				cfg.Streaming.KeepAliveBeforeFirstChunk = enabled
				cfg.Streaming.KeepAliveSeconds = 30
				h := NewBaseAPIHandlers(cfg, nil)
				_, s, _ = h.PrepareHTTPStream(s.c, context.Background(), func(...interface{}) {}, "openai", sse)
				defer s.Close()
				want := defaultFirstChunkTimeout
				if timeout > 0 {
					want = time.Duration(timeout) * time.Second
				}
				if s.wait != want || (s.Heartbeats() != nil) != (enabled && sse) {
					t.Fatalf("timeout=%d enabled=%v sse=%v: wait=%v heartbeats=%v", timeout, enabled, sse, s.wait, s.Heartbeats() != nil)
				}
			}
		}
	}
}

func TestHTTPStreamStartupMeaningfulOutput(t *testing.T) {
	cases := []struct{ name, protocol, metadata, output string }{
		{"chat text", "openai", `{"choices":[{"delta":{"role":"assistant","content":""}}]}`, `{"choices":[{"delta":{"content":"hi"}}]}`},
		{"chat reasoning", "openai", `{"choices":[]}`, `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`},
		{"chat tool", "openai", `{"choices":[]}`, `{"choices":[{"delta":{"tool_calls":[{"function":{"name":"read"}}]}}]}`},
		{"chat terminal", "openai", `{"usage":{"output_tokens":0}}`, `{"choices":[{"delta":{},"finish_reason":"stop"}]}`},
		{"claude thinking", "claude", "data: {\"type\":\"message_start\"}\n\n", "data: {\"type\":\"content_block_delta\",\"delta\":{\"thinking\":\"think\"}}\n\n"},
		{"claude terminal", "claude", "event: ping\ndata: {\"type\":\"ping\"}\n\n", "data: {\"type\":\"message_stop\"}\n\n"},
		{"responses text", "openai-response", "data: {\"type\":\"response.created\"}\n\n", "data: {\"type\":\"response.output_text.delta\",\r\ndata: \"delta\":\"hi\"}\r\n\r\n"},
		{"responses tool", "openai-response", "data: {\"type\":\"response.in_progress\"}\n\n", "data: {\"type\":\"response.output_item.added\",\"item\":{\"type\":\"function_call\",\"name\":\"read\"}}\n\n"},
		{"responses terminal", "openai-response", ": keep-alive\n\n", "data: {\"type\":\"response.completed\"}\n\n"},
		{"gemini text", "gemini", `{"candidates":[{"content":{"role":"model","parts":[]}}]}`, `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}]}`},
		{"gemini terminal", "gemini", `{"usageMetadata":{}}`, `{"candidates":[{"finishReason":"STOP"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := testHTTPStartup(t, tc.protocol, true)
			original := s.Timeout()
			s.Observe([]byte(tc.metadata))
			if s.Timeout() != original {
				t.Fatal("metadata reset or disabled budget")
			}
			if tc.protocol == "claude" || tc.protocol == "openai-response" {
				midpoint := len(tc.output) / 2
				s.Observe([]byte(tc.output[:midpoint]))
				if s.Timeout() == nil {
					t.Fatal("partial SSE event accepted")
				}
				s.Observe([]byte(tc.output[midpoint:]))
			} else {
				s.Observe([]byte(tc.output))
			}
			if s.Timeout() != nil {
				t.Fatal("model output did not end budget")
			}
		})
	}
}

func TestHTTPStreamStartupHeartbeatDoesNotConsumeBudgetOrLateHeaders(t *testing.T) {
	s, r := testHTTPStartup(t, "openai", true)
	original := s.Timeout()
	if err := s.WriteHeartbeat(); err != nil {
		t.Fatal(err)
	}
	s.SetHeaders(http.Header{"X-Late": []string{"hidden"}, "Content-Type": []string{"application/json"}})
	if s.Timeout() != original || r.Body.String() != ": keep-alive\n\n" || r.Result().Header.Get("X-Late") != "" || r.Result().Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal("heartbeat affected budget or committed headers")
	}
}

func TestHTTPStreamStartupTimeoutDuringSynchronousExecute(t *testing.T) {
	for _, heartbeat := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP504", true: "SSEerror"}[heartbeat], func(t *testing.T) {
			s, r := testHTTPStartup(t, "openai", true)
			s.timer.Stop()
			ticks := make(chan time.Time, 1)
			s.timeout = ticks
			if heartbeat {
				if err := s.WriteHeartbeat(); err != nil {
					t.Fatal(err)
				}
			}
			entered := make(chan struct{})
			exited := make(chan struct{})
			go func() { <-entered; ticks <- time.Time{} }()
			_, _, errs := s.Execute(func(ctx context.Context) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
				close(entered)
				<-ctx.Done()
				close(exited)
				return nil, nil, nil
			})
			msg := <-errs
			if msg == nil || msg.StatusCode != 504 || !errors.Is(msg.Error, context.DeadlineExceeded) {
				t.Fatalf("timeout=%+v", msg)
			}
			h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h.WriteErrorResponse(s.c, msg)
			<-exited
			if heartbeat {
				if r.Code != 200 || !strings.Contains(r.Body.String(), "data: {\"error\"") {
					t.Fatalf("status=%d body=%s", r.Code, r.Body.String())
				}
			} else if r.Code != 504 {
				t.Fatalf("status=%d", r.Code)
			}
			s.finishDelivery(msg.Error)
			outcome, status, err := streamDeliveryCompletion(s.ctx, pluginapi.RequestCompletionCanceled, 0, context.Canceled)
			if outcome != pluginapi.RequestCompletionFailed || status != 504 || firstChunkFailure(s.ctx) != err {
				t.Fatalf("completion=%v %d %v", outcome, status, err)
			}
			if _, ok := s.c.Get("API_RESPONSE_ERROR"); !ok {
				t.Fatal("timeout missing from error-only logging")
			}
		})
	}
}

func TestHTTPStreamStartupTimeoutAfterMetadata(t *testing.T) {
	s, r := testHTTPStartup(t, "openai", true)
	s.timer.Stop()
	ticks := make(chan time.Time, 1)
	s.timeout = ticks
	metadata := []byte(`{"choices":[{"delta":{"role":"assistant"}}]}`)
	s.Observe(metadata)
	if err := s.WriteHeartbeat(); err != nil {
		t.Fatal(err)
	}
	ticks <- time.Time{}
	var failure error
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h.ForwardStream(s.c, r, func(err error) { failure = err; s.finishDelivery(err) }, make(chan []byte), make(chan *interfaces.ErrorMessage), StreamForwardOptions{WriteTerminalError: func(msg *interfaces.ErrorMessage) { s.writeCommittedError(msg) }})
	if !errors.Is(failure, context.DeadlineExceeded) || strings.Contains(r.Body.String(), "[DONE]") || !strings.Contains(r.Body.String(), "timed out") {
		t.Fatalf("failure=%v body=%s", failure, r.Body.String())
	}
}

func TestHTTPStreamStartupMetadataEOFIsFailure(t *testing.T) {
	s, _ := testHTTPStartup(t, "openai", true)
	s.Observe([]byte(`{"choices":[{"delta":{"role":"assistant"}}]}`))
	if msg, ok := s.PendingCloseError(nil); !ok || msg.StatusCode != 502 {
		t.Fatalf("EOF=%+v %v", msg, ok)
	}
}

func TestHTTPStreamStartupExecutePanicReturnsError(t *testing.T) {
	s, _ := testHTTPStartup(t, "openai", true)
	_, _, errs := s.Execute(func(context.Context) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		panic("secret upstream panic")
	})
	msg := <-errs
	if msg.StatusCode != 500 || strings.Contains(msg.Error.Error(), "secret") {
		t.Fatalf("panic result=%+v", msg)
	}
}

func TestHTTPStreamStartupRequestSnapshotAndLogs(t *testing.T) {
	s, _ := testHTTPStartup(t, "openai", true)
	entered := make(chan struct{})
	release := make(chan struct{})
	go func() {
		<-entered
		s.c.Request = httptest.NewRequest(http.MethodGet, "/recycled", nil)
		s.c.Set("new-request", true)
		close(release)
	}()
	_, _, errs := s.Execute(func(ctx context.Context) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
		snapshot := ctx.Value("gin").(*gin.Context)
		close(entered)
		<-release
		if snapshot == s.c || snapshot.Request.URL.Path != "/stream" {
			return s.errorChannels(errors.New("execution retained live Gin context"))
		}
		snapshot.Set("API_REQUEST", []byte("fixture request"))
		snapshot.Set("API_RESPONSE_ERROR", []*interfaces.ErrorMessage{{StatusCode: 502, Error: errors.New("fixture error")}})
		out := make(chan *interfaces.ErrorMessage)
		close(out)
		return nil, nil, out
	})
	if msg, ok := PendingStreamError(errs); ok {
		t.Fatal(msg.Error)
	}
	s.copyExecutionLogs()
	if request, _ := s.c.Get("API_REQUEST"); string(request.([]byte)) != "fixture request" {
		t.Fatal(request)
	}
	if errs, _ := s.c.Get("API_RESPONSE_ERROR"); len(errs.([]*interfaces.ErrorMessage)) != 1 {
		t.Fatal("logs duplicated", errs)
	}
}

func TestHTTPStreamStartupAcknowledgesDeliveryBeforeCancellation(t *testing.T) {
	s, _ := testHTTPStartup(t, "openai", true)
	s.Close()
	h := NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	var execution context.Context
	var cancel APIHandlerCancelFunc
	execution, s, cancel = h.PrepareHTTPStream(s.c, context.Background(), func(...interface{}) {
		if execution.Err() != nil {
			t.Error("execution canceled before HTTP acknowledgment")
		}
	}, "openai", true)
	defer s.Close()
	cancel(errors.New("fixture transport failure"))
	if execution.Err() != context.Canceled {
		t.Fatal("execution was not canceled")
	}
}

func TestHTTPStreamStartupCopiesUpdatedLogsWithoutOverwritingConsumerError(t *testing.T) {
	s, _ := testHTTPStartup(t, "openai", true)
	s.snapshot.Set("API_RESPONSE", []byte("first"))
	s.copyExecutionLogs()
	s.snapshot.Set("API_RESPONSE", []byte("complete response"))
	s.copyExecutionLogs()
	if body, _ := s.c.Get("API_RESPONSE"); string(body.([]byte)) != "complete response" {
		t.Fatal("upstream logs truncated", body)
	}
	s.c.Set("API_RESPONSE", []byte("consumer error"))
	s.copyExecutionLogs()
	if body, _ := s.c.Get("API_RESPONSE"); string(body.([]byte)) != "consumer error" {
		t.Fatal("consumer failure overwritten", body)
	}
}
