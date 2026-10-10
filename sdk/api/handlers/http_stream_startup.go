package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type httpStreamContextKey struct{}

func validateHTTPStreamSSE(ctx context.Context, protocol string) bool {
	return protocol == "openai-response" || (protocol == "claude" && ctx != nil && ctx.Value(httpStreamContextKey{}) == true)
}

const httpStreamStartupKey = "__http_stream_startup__"
const defaultFirstChunkTimeout = 360 * time.Second

// firstChunkTimeoutError is a generation failure, not a client disconnect.
type firstChunkTimeoutError struct{ wait time.Duration }

func (e *firstChunkTimeoutError) Error() string {
	return fmt.Sprintf("upstream first model output timed out after %s", e.wait)
}
func (*firstChunkTimeoutError) StatusCode() int { return http.StatusGatewayTimeout }
func (*firstChunkTimeoutError) Unwrap() error   { return context.DeadlineExceeded }

func firstChunkFailure(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	var timeout *firstChunkTimeoutError
	if cause := context.Cause(ctx); errors.As(cause, &timeout) {
		return cause
	}
	return nil
}

// HTTPStreamStartup belongs to the HTTP consumer. Only that goroutine may write
// headers/body or mutate timers; the execution worker never uses its writer.
type HTTPStreamStartup struct {
	c              *gin.Context
	ctx            context.Context
	requestCtx     context.Context
	cancel         context.CancelCauseFunc
	finishDelivery func(error)
	snapshot       *gin.Context
	protocol       string
	sse            bool
	ticker         *time.Ticker
	ticks          <-chan time.Time
	timer          *time.Timer
	timeout        <-chan time.Time
	wait           time.Duration
	errorWriter    func(*interfaces.ErrorMessage)
	pending        []byte
	copiedErrors   int
	copiedResponse []byte
}

// PrepareHTTPStream adds a finite first-output budget and optional early SSE
// heartbeats to an HTTP text stream. It leaves WebSocket/internal execution alone.
func (h *BaseAPIHandler) PrepareHTTPStream(c *gin.Context, ctx context.Context, cancel APIHandlerCancelFunc, protocol string, sse bool) (context.Context, *HTTPStreamStartup, APIHandlerCancelFunc) {
	wait := defaultFirstChunkTimeout
	if h.Cfg != nil && h.Cfg.Streaming.FirstChunkTimeoutSeconds > 0 {
		wait = time.Duration(h.Cfg.Streaming.FirstChunkTimeoutSeconds) * time.Second
		if wait <= 0 {
			wait = defaultFirstChunkTimeout
		}
	}
	// Initialize shared trace state before copying the Gin request. The callback
	// stores only into its synchronized state and never retains a response writer.
	_ = logging.GinCPATraceIDCallback(c)
	snapshot := c.Copy()
	ctx = context.WithValue(ctx, httpStreamContextKey{}, true)
	ctx = context.WithValue(ctx, "gin", snapshot)
	finish := func(error) {}
	if !coreusage.StreamDeliveryTracked(ctx) {
		ctx, finish = coreusage.WithStreamDelivery(ctx)
	}
	ctx, stop := context.WithCancelCause(ctx)
	if c.Request != nil {
		snapshot.Request = c.Request.Clone(ctx)
	}
	s := &HTTPStreamStartup{c: c, ctx: ctx, requestCtx: c.Request.Context(), cancel: stop, finishDelivery: finish, snapshot: snapshot, protocol: protocol, sse: sse, wait: wait}
	s.timer = time.NewTimer(wait)
	s.timeout = s.timer.C
	if h.Cfg != nil && sse && h.Cfg.Streaming.KeepAliveBeforeFirstChunk {
		if interval := StreamingKeepAliveInterval(h.Cfg); interval > 0 {
			s.ticker = time.NewTicker(interval)
			s.ticks = s.ticker.C
		}
	}
	c.Set(httpStreamStartupKey, s)
	return ctx, s, func(params ...interface{}) {
		var err error
		if len(params) > 0 {
			err, _ = params[0].(error)
		}
		s.copyExecutionLogs()
		s.finishDelivery(err)
		cancel(params...)
		s.cancel(err)
	}
}

func httpStreamStartup(c *gin.Context) *HTTPStreamStartup {
	if c == nil {
		return nil
	}
	value, _ := c.Get(httpStreamStartupKey)
	s, _ := value.(*HTTPStreamStartup)
	return s
}

// WriteCommittedHTTPStreamError switches from an HTTP error to a protocol error
// after the first heartbeat has committed the response.
func WriteCommittedHTTPStreamError(c *gin.Context, msg *interfaces.ErrorMessage) bool {
	if s := httpStreamStartup(c); s != nil {
		return s.writeCommittedError(msg)
	}
	return false
}

func (s *HTTPStreamStartup) PendingCloseError(errs <-chan *interfaces.ErrorMessage) (*interfaces.ErrorMessage, bool) {
	if msg, ok := PendingStreamError(errs); ok {
		return msg, true
	}
	if s.timeout != nil {
		return &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("upstream stream closed before first model output")}, true
	}
	return nil, false
}

// Close guarantees producer cancellation even when a transport write fails or
// an entrypoint returns early. Snapshot logs can safely outlive Gin recycling.
func (s *HTTPStreamStartup) Close() {
	s.timer.Stop()
	if s.ticker != nil {
		s.ticker.Stop()
	}
	s.finishDelivery(context.Canceled)
	s.cancel(context.Canceled)
	s.copyExecutionLogs()
}

func (s *HTTPStreamStartup) copyExecutionLogs() {
	snapshot := s.snapshot.Copy()
	for key, value := range snapshot.Keys {
		if key == "API_RESPONSE_ERROR" {
			if errs, ok := value.([]*interfaces.ErrorMessage); ok && len(errs) > s.copiedErrors {
				existing, _ := s.c.Get(key)
				previous, _ := existing.([]*interfaces.ErrorMessage)
				merged := append(append([]*interfaces.ErrorMessage(nil), previous...), errs[s.copiedErrors:]...)
				s.c.Set(key, merged)
				s.copiedErrors = len(errs)
			}
		} else if strings.HasPrefix(key, "API_") {
			if key == "API_RESPONSE" {
				if existing, exists := s.c.Get(key); exists {
					body, ok := existing.([]byte)
					if !ok || !bytes.Equal(body, s.copiedResponse) {
						continue
					}
				}
				if body, ok := value.([]byte); ok {
					s.copiedResponse = bytes.Clone(body)
				}
			}
			if body, ok := value.([]byte); ok {
				value = bytes.Clone(body)
			}
			s.c.Set(key, value)
		}
	}
}

// Execute waits in the consumer while the existing synchronous executor and
// bootstrap retry pipeline run in a worker. A buffered result cannot strand a
// worker when the client disconnects or the first-output budget expires.
func (s *HTTPStreamStartup) Execute(run func(context.Context) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage)) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	type result struct {
		data    <-chan []byte
		headers http.Header
		errs    <-chan *interfaces.ErrorMessage
	}
	ready := make(chan result, 1)
	go func() {
		defer func() {
			if recover() != nil {
				log.Errorf("upstream stream execution panicked: %s", debug.Stack())
				data, headers, errs := s.errorChannels(errors.New("upstream stream execution panicked"))
				ready <- result{data, headers, errs}
			}
		}()
		data, headers, errs := run(s.ctx)
		ready <- result{data, headers, errs}
	}()
	for {
		select {
		case r := <-ready:
			s.copyExecutionLogs()
			return r.data, r.headers, r.errs
		case <-s.requestCtx.Done():
			s.cancel(s.requestCtx.Err())
			return s.errorChannels(s.requestCtx.Err())
		case <-s.timeout:
			msg := s.TimeoutError()
			return s.errorChannels(msg.Error)
		case <-s.ticks:
			if err := s.WriteHeartbeat(); err != nil {
				s.cancel(err)
				return s.errorChannels(err)
			}
		}
	}
}

func (s *HTTPStreamStartup) errorChannels(err error) (<-chan []byte, http.Header, <-chan *interfaces.ErrorMessage) {
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- executionErrorMessage(err)
	close(errs)
	return nil, nil, errs
}

func (s *HTTPStreamStartup) Heartbeats() <-chan time.Time { return s.ticks }
func (s *HTTPStreamStartup) Timeout() <-chan time.Time    { return s.timeout }
func (s *HTTPStreamStartup) TimeoutError() *interfaces.ErrorMessage {
	s.timeout = nil
	err := &firstChunkTimeoutError{wait: s.wait}
	s.cancel(err)
	return &interfaces.ErrorMessage{StatusCode: http.StatusGatewayTimeout, Error: err}
}

func (s *HTTPStreamStartup) SetErrorWriter(write func(*interfaces.ErrorMessage)) {
	s.errorWriter = write
}

// SetHeaders commits only transport headers known at this point. Late upstream
// headers cannot be appended to an already-flushed response.
func (s *HTTPStreamStartup) SetHeaders(upstream http.Header) {
	if s.c.Writer.Written() {
		return
	}
	if s.sse {
		s.c.Header("Content-Type", "text/event-stream")
		s.c.Header("Cache-Control", "no-cache")
		s.c.Header("Connection", "keep-alive")
		s.c.Header("Access-Control-Allow-Origin", "*")
		s.c.Writer.Header().Del("Content-Length")
	}
	WriteUpstreamHeaders(s.c.Writer.Header(), upstream)
	// SSE framing owns content type and length, including intercepted headers.
	if s.sse {
		s.c.Writer.Header().Del("Content-Length")
		s.c.Header("Content-Type", "text/event-stream")
	}
}

// WritePayload reports transport failures before accepting model output.
func (s *HTTPStreamStartup) WritePayload(payload []byte) error {
	if n, err := s.c.Writer.Write(payload); err != nil {
		return err
	} else if n != len(payload) {
		return io.ErrShortWrite
	}
	return httpwire.FlushResponse(s.c.Writer)
}

func (s *HTTPStreamStartup) WriteHeartbeat() error {
	s.SetHeaders(nil)
	return s.WritePayload([]byte(": keep-alive\n\n"))
}

func (s *HTTPStreamStartup) recordFailure(msg *interfaces.ErrorMessage) {
	if msg == nil {
		return
	}
	existing, _ := s.c.Get("API_RESPONSE_ERROR")
	errs, _ := existing.([]*interfaces.ErrorMessage)
	s.c.Set("API_RESPONSE_ERROR", append(errs, msg))
}

func (s *HTTPStreamStartup) writeCommittedError(msg *interfaces.ErrorMessage) bool {
	s.recordFailure(msg)
	if !s.c.Writer.Written() || !s.sse {
		return false
	}
	if s.requestCtx.Err() != nil {
		return true
	}
	if s.errorWriter != nil {
		s.errorWriter(msg)
	} else {
		status := http.StatusInternalServerError
		message := http.StatusText(status)
		if msg != nil {
			if msg.StatusCode > 0 {
				status = msg.StatusCode
			}
			if msg.Error != nil {
				message = msg.Error.Error()
			}
		}
		body := BuildErrorResponseBody(status, message)
		if s.protocol == "gemini" {
			_, _ = fmt.Fprintf(s.c.Writer, "event: error\ndata: %s\n\n", body)
		} else {
			_, _ = fmt.Fprintf(s.c.Writer, "data: %s\n\n", body)
		}
	}
	if err := httpwire.FlushResponse(s.c.Writer); err != nil {
		s.cancel(err)
	}
	return true
}

// Observe runs only after a successful write/flush. Metadata and SSE comments
// never satisfy the first-model-output budget. Fragments are buffered solely
// until a complete event can be examined; they are not delivered to usage.
func (s *HTTPStreamStartup) Observe(payload []byte) {
	if s.timeout == nil {
		return
	}
	if s.protocol == "openai" || s.protocol == "gemini" {
		if meaningfulStreamPayload(s.protocol, payload) {
			s.markOutput()
		}
		return
	}
	if len(s.pending)+len(payload) > 1<<20 {
		return
	}
	s.pending = append(s.pending, payload...)
	for {
		end := bytes.Index(s.pending, []byte("\n\n"))
		width := 2
		if crlf := bytes.Index(s.pending, []byte("\r\n\r\n")); crlf >= 0 && (end < 0 || crlf < end) {
			end = crlf
			width = 4
		}
		if end < 0 {
			return
		}
		event := s.pending[:end]
		s.pending = s.pending[end+width:]
		var data []byte
		for _, line := range bytes.Split(event, []byte("\n")) {
			line = bytes.TrimSuffix(line, []byte("\r"))
			if bytes.HasPrefix(line, []byte("data:")) {
				if len(data) > 0 {
					data = append(data, '\n')
				}
				data = append(data, bytes.TrimPrefix(line[5:], []byte(" "))...)
			}
		}
		if meaningfulStreamPayload(s.protocol, data) {
			s.markOutput()
			return
		}
	}
}
func (s *HTTPStreamStartup) markOutput() { s.timer.Stop(); s.timeout = nil; s.pending = nil }

func meaningfulStreamPayload(protocol string, payload []byte) bool {
	if !gjson.ValidBytes(payload) {
		return false
	}
	node := gjson.ParseBytes(payload)
	nonempty := func(n gjson.Result) bool { return n.Type == gjson.String && n.Str != "" }
	output := func(n gjson.Result) bool {
		for _, key := range []string{"content", "text", "thinking", "reasoning_content", "reasoning", "partial_json", "signature", "audio.data"} {
			if nonempty(n.Get(key)) {
				return true
			}
		}
		return false
	}
	switch protocol {
	case "openai":
		for _, choice := range node.Get("choices").Array() {
			if output(choice.Get("delta")) || output(choice) || nonempty(choice.Get("finish_reason")) || nonempty(choice.Get("delta.function_call.name")) || nonempty(choice.Get("delta.function_call.arguments")) {
				return true
			}
			for _, call := range choice.Get("delta.tool_calls").Array() {
				if nonempty(call.Get("id")) || nonempty(call.Get("function.name")) || nonempty(call.Get("function.arguments")) {
					return true
				}
			}
		}
	case "claude":
		typ := node.Get("type").String()
		if typ == "message_stop" {
			return true
		}
		if typ == "content_block_delta" {
			return output(node.Get("delta"))
		}
		if typ == "content_block_start" {
			block := node.Get("content_block")
			return output(block) || (block.Get("type").String() == "tool_use" && nonempty(block.Get("name")))
		}
	case "openai-response":
		typ := node.Get("type").String()
		if typ == "response.completed" || typ == "response.incomplete" {
			return true
		}
		if strings.HasSuffix(typ, ".delta") && nonempty(node.Get("delta")) {
			return true
		}
		item := node.Get("item")
		if item.Get("type").String() == "function_call" && nonempty(item.Get("name")) {
			return true
		}
	case "gemini":
		for _, candidate := range node.Get("candidates").Array() {
			if nonempty(candidate.Get("finishReason")) {
				return true
			}
			for _, part := range candidate.Get("content.parts").Array() {
				if nonempty(part.Get("text")) || nonempty(part.Get("functionCall.name")) || nonempty(part.Get("inlineData.data")) {
					return true
				}
			}
		}
	}
	return false
}
