package test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/gemini"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type startupExecutor struct {
	run func(context.Context, *coreauth.Auth) (*coreexecutor.StreamResult, error)
}

func (*startupExecutor) Identifier() string { return "startup-fixture" }
func (*startupExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unused")
}
func (e *startupExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return e.run(ctx, auth)
}
func (*startupExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}
func (*startupExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unused")
}
func (*startupExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unused")
}

type startupRoute struct {
	name, path, body, output string
	endpoint                 func(*handlers.BaseAPIHandler) gin.HandlerFunc
}

var startupRoutes = []startupRoute{
	{"chat", "/v1/chat/completions", `{"model":"%s","messages":[{"role":"user","content":"hi"}],"stream":true}`, `{"choices":[{"index":0,"delta":{"content":"fixture-OK"},"finish_reason":"stop"}]}`, func(h *handlers.BaseAPIHandler) gin.HandlerFunc { return openai.NewOpenAIAPIHandler(h).ChatCompletions }},
	{"completions", "/v1/completions", `{"model":"%s","prompt":"hi","stream":true}`, `{"choices":[{"index":0,"delta":{"content":"fixture-OK"},"finish_reason":"stop"}]}`, func(h *handlers.BaseAPIHandler) gin.HandlerFunc { return openai.NewOpenAIAPIHandler(h).Completions }},
	{"responses", "/v1/responses", `{"model":"%s","input":"hi","stream":true}`, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"fixture\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"fixture-OK\"}]}]}}\n\n", func(h *handlers.BaseAPIHandler) gin.HandlerFunc {
		return openai.NewOpenAIResponsesAPIHandler(h).Responses
	}},
	{"claude", "/v1/messages", `{"model":"%s","max_tokens":10,"messages":[{"role":"user","content":"hi"}],"stream":true}`, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture-OK\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", func(h *handlers.BaseAPIHandler) gin.HandlerFunc {
		return claude.NewClaudeCodeAPIHandler(h).ClaudeMessages
	}},
	{"gemini", "/v1beta/models/:action", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"fixture":"%s"}`, `{"candidates":[{"content":{"parts":[{"text":"fixture-OK"}]},"finishReason":"STOP"}]}`, func(h *handlers.BaseAPIHandler) gin.HandlerFunc { return gemini.NewGeminiAPIHandler(h).GeminiHandler }},
}

func startupServer(t *testing.T, route startupRoute, cfg *sdkconfig.SDKConfig, run func(context.Context, *coreauth.Auth) (*coreexecutor.StreamResult, error)) (*httptest.Server, string, string) {
	t.Helper()
	model := "startup-" + strings.NewReplacer("/", "-", "_", "-").Replace(t.Name())
	executor := &startupExecutor{run: run}
	manager := coreauth.NewManager(nil, &coreauth.FillFirstSelector{}, nil)
	manager.RegisterExecutor(executor)
	for _, id := range []string{model + "-a", model + "-b"} {
		_, err := manager.Register(context.Background(), &coreauth.Auth{ID: id, Provider: executor.Identifier(), Status: coreauth.StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		registry.GetGlobalRegistry().RegisterClient(id, executor.Identifier(), []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	router := gin.New()
	router.POST(route.path, route.endpoint(handlers.NewBaseAPIHandlers(cfg, manager)))
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	path := route.path
	if route.name == "gemini" {
		path = "/v1beta/models/" + model + ":streamGenerateContent"
	}
	return server, path, fmt.Sprintf(route.body, model)
}

func startupRequest(t *testing.T, server *httptest.Server, path, body string) *http.Response {
	t.Helper()
	client := server.Client()
	client.Timeout = 10 * time.Second
	resp, err := client.Post(server.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

func TestHTTPStreamStartupEndpointHeartbeats(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, route := range startupRoutes {
		t.Run(route.name, func(t *testing.T) {
			for _, phase := range []string{"executor", "bootstrap"} {
				t.Run(phase, func(t *testing.T) {
					cfg := &sdkconfig.SDKConfig{}
					cfg.Streaming.KeepAliveSeconds = 1
					cfg.Streaming.KeepAliveBeforeFirstChunk = true
					release := make(chan struct{})
					var once sync.Once
					defer once.Do(func() { close(release) })
					server, path, body := startupServer(t, route, cfg, func(ctx context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
						chunks := make(chan coreexecutor.StreamChunk, 1)
						if phase == "executor" {
							select {
							case <-release:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
							chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output)}
							close(chunks)
						} else {
							go func() {
								defer close(chunks)
								select {
								case <-release:
									chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output)}
								case <-ctx.Done():
								}
							}()
						}
						return &coreexecutor.StreamResult{Chunks: chunks}, nil
					})
					resp := startupRequest(t, server, path, body)
					if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
						t.Fatalf("status=%d headers=%v", resp.StatusCode, resp.Header)
					}
					reader := bufio.NewReader(resp.Body)
					first, err := reader.ReadString('\n')
					if err != nil {
						t.Fatal(err)
					}
					if first != ": keep-alive\n" {
						t.Fatalf("first bytes=%q", first)
					}
					once.Do(func() { close(release) })
					rest, err := io.ReadAll(reader)
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(rest), "fixture-OK") {
						t.Fatalf("missing final output: %s", rest)
					}
				})
			}
		})
	}
}

func TestHTTPStreamStartupEndpointTimeout(t *testing.T) {
	for _, route := range startupRoutes {
		t.Run(route.name, func(t *testing.T) {
			cfg := &sdkconfig.SDKConfig{}
			cfg.Streaming.KeepAliveSeconds = 1
			cfg.Streaming.KeepAliveBeforeFirstChunk = true
			cfg.Streaming.FirstChunkTimeoutSeconds = 2
			stopped := make(chan struct{})
			var once sync.Once
			server, path, body := startupServer(t, route, cfg, func(ctx context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
				<-ctx.Done()
				once.Do(func() { close(stopped) })
				return nil, ctx.Err()
			})
			resp := startupRequest(t, server, path, body)
			output, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || !strings.Contains(string(output), "keep-alive") || !strings.Contains(string(output), "timed out") || strings.Contains(string(output), "[DONE]") {
				t.Fatalf("status=%d body=%s", resp.StatusCode, output)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("executor was not canceled")
			}
		})
	}
}

type startupUnavailable struct{}

func (startupUnavailable) Error() string   { return "fictional account unavailable" }
func (startupUnavailable) StatusCode() int { return 503 }

func TestHTTPStreamStartupHeartbeatAllowsAccountFallback(t *testing.T) {
	route := startupRoutes[0]
	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 1
	cfg.Streaming.KeepAliveBeforeFirstChunk = true
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	var mu sync.Mutex
	var accounts []string
	server, path, body := startupServer(t, route, cfg, func(ctx context.Context, auth *coreauth.Auth) (*coreexecutor.StreamResult, error) {
		mu.Lock()
		accounts = append(accounts, auth.ID)
		attempt := len(accounts)
		mu.Unlock()
		if attempt == 1 {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			return nil, startupUnavailable{}
		}
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output)}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	})
	resp := startupRequest(t, server, path, body)
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	if err != nil || line != ": keep-alive\n" {
		t.Fatalf("heartbeat=%q %v", line, err)
	}
	once.Do(func() { close(release) })
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(string(output), "fixture-OK") || len(accounts) != 2 || accounts[0] == accounts[1] {
		t.Fatalf("output=%s accounts=%v", output, accounts)
	}
}

func TestHTTPStreamStartupFirstOutputEndsBudget(t *testing.T) {
	route := startupRoutes[0]
	cfg := &sdkconfig.SDKConfig{}
	cfg.Streaming.KeepAliveSeconds = 2
	cfg.Streaming.KeepAliveBeforeFirstChunk = true
	cfg.Streaming.FirstChunkTimeoutSeconds = 1
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	server, path, body := startupServer(t, route, cfg, func(ctx context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
		chunks := make(chan coreexecutor.StreamChunk, 1)
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"choices":[{"index":0,"delta":{"reasoning_content":"fixture-thinking"},"finish_reason":null}]}`)}
		go func() {
			defer close(chunks)
			select {
			case <-release:
				chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output)}
			case <-ctx.Done():
			}
		}()
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	})
	resp := startupRequest(t, server, path, body)
	reader := bufio.NewReader(resp.Body)
	var output strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal("stream ended before post-budget heartbeat", err, output.String())
		}
		output.WriteString(line)
		if line == ": keep-alive\n" {
			break
		}
	}
	once.Do(func() { close(release) })
	tail, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	output.Write(tail)
	if !strings.Contains(output.String(), "fixture-thinking") || !strings.Contains(output.String(), "fixture-OK") || strings.Contains(output.String(), "timed out") {
		t.Fatal(output.String())
	}
}

func TestHTTPStreamStartupHeartbeatBetweenSSEFragments(t *testing.T) {
	for _, route := range []startupRoute{startupRoutes[2], startupRoutes[3]} {
		t.Run(route.name, func(t *testing.T) {
			cfg := &sdkconfig.SDKConfig{}
			cfg.Streaming.KeepAliveSeconds = 1
			cfg.Streaming.KeepAliveBeforeFirstChunk = true
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			server, path, body := startupServer(t, route, cfg, func(ctx context.Context, _ *coreauth.Auth) (*coreexecutor.StreamResult, error) {
				chunks := make(chan coreexecutor.StreamChunk, 1)
				split := len(route.output) / 3
				chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output[:split])}
				go func() {
					defer close(chunks)
					select {
					case <-release:
						chunks <- coreexecutor.StreamChunk{Payload: []byte(route.output[split:])}
					case <-ctx.Done():
					}
				}()
				return &coreexecutor.StreamResult{Chunks: chunks}, nil
			})
			resp := startupRequest(t, server, path, body)
			reader := bufio.NewReader(resp.Body)
			line, err := reader.ReadString('\n')
			if err != nil || line != ": keep-alive\n" {
				t.Fatalf("fragment leaked before heartbeat: %q %v", line, err)
			}
			once.Do(func() { close(release) })
			output, err := io.ReadAll(reader)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(output), "fixture-OK") || strings.Contains(string(output), "\"error\"") {
				t.Fatalf("corrupted SSE: %s", output)
			}
		})
	}
}
