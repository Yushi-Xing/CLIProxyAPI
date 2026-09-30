package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityExecutorChatToolImagesReachUpstream(t *testing.T) {
	for _, model := range []string{"gemini-3-flash", "claude-sonnet-4-6"} {
		for _, stream := range []bool{false, true} {
			name := model + "/non-streaming"
			if stream {
				name = model + "/streaming"
			}
			t.Run(name, func(t *testing.T) {
				captured := make(chan []byte, 1)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					body, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read upstream request: %v", errRead)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					select {
					case captured <- body:
					default:
						t.Error("unexpected additional upstream request")
					}
					response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"image received"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":2,"totalTokenCount":3}}}`
					if strings.Contains(r.URL.Path, "streamGenerateContent") {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, "data: "+response+"\n\n")
					} else {
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, response)
					}
				}))
				defer server.Close()

				auth := &cliproxyauth.Auth{
					Attributes: map[string]string{"base_url": server.URL},
					Metadata: map[string]any{
						"access_token": "test-token",
						"expired":      time.Now().Add(time.Hour).Format(time.RFC3339),
						"project_id":   "test-project",
					},
				}
				request := cliproxyexecutor.Request{
					Model: model,
					Payload: []byte(`{"messages":[
						{"role":"user","content":"inspect the image and read the metadata"},
						{"role":"assistant","tool_calls":[
							{"id":"call_image","type":"function","function":{"name":"view_image","arguments":"{}"}},
							{"id":"call_text","type":"function","function":{"name":"read_file","arguments":"{}"}}]},
						{"role":"tool","tool_call_id":"call_text","content":"{\"width\":1}"},
						{"role":"tool","tool_call_id":"call_image","content":[
							{"type":"text","text":"Screenshot taken"},
							{"type":"image_url","image_url":{"url":"data:image/png;base64,aW1hZ2U="}}]}],
						"tools":[
							{"type":"function","function":{"name":"view_image","parameters":{"type":"object","properties":{}}}},
							{"type":"function","function":{"name":"read_file","parameters":{"type":"object","properties":{}}}}]}`),
				}
				opts := cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatOpenAI, Stream: stream,
				}
				executor := NewAntigravityExecutor(&config.Config{})
				if stream {
					result, errExecute := executor.ExecuteStream(context.Background(), auth, request, opts)
					if errExecute != nil {
						t.Fatalf("ExecuteStream: %v", errExecute)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("stream chunk: %v", chunk.Err)
						}
					}
				} else if _, errExecute := executor.Execute(context.Background(), auth, request, opts); errExecute != nil {
					t.Fatalf("Execute: %v", errExecute)
				}

				var body []byte
				select {
				case body = <-captured:
				default:
					t.Fatal("executor did not send an upstream request")
				}
				responses := make(map[string]gjson.Result)
				for _, content := range gjson.GetBytes(body, "request.contents").Array() {
					for _, part := range content.Get("parts").Array() {
						if response := part.Get("functionResponse"); response.Exists() {
							responses[response.Get("id").String()] = response
						}
					}
				}
				if len(responses) != 2 {
					t.Fatalf("upstream tool response count = %d, want 2", len(responses))
				}
				image := responses["call_image"]
				if image.Get("name").String() != "view_image" || image.Get("response.result").String() != `[{"type":"text","text":"Screenshot taken"}]` {
					t.Fatal("image tool identity or text changed")
				}
				parts := image.Get("parts").Array()
				if len(parts) != 1 || parts[0].Get("inlineData.mimeType").String() != "image/png" || parts[0].Get("inlineData.data").String() != "aW1hZ2U=" {
					t.Fatal("native tool image missing from final upstream request")
				}
				text := responses["call_text"]
				if text.Get("name").String() != "read_file" || text.Get("response.result").Type != gjson.String || text.Get("response.result").String() != `{"width":1}` || text.Get("parts").Exists() {
					t.Fatal("plain tool result changed or acquired another tool's image")
				}
				if got := strings.Count(string(body), "aW1hZ2U="); got != 1 {
					t.Fatalf("base64 payload appears %d times in upstream request, want once", got)
				}
			})
		}
	}
}
