# HTTP stream startup liveness

Enable early SSE heartbeats for long-thinking models behind an idle read timeout:

```yaml
requests:
  streaming:
    keepalive-seconds: 30
    keepalive-before-first-chunk: true
    first-chunk-timeout-seconds: 360
```

The first-output budget defaults to 360 seconds, including when configured as zero or negative. It starts before executor/auth selection and applies across attempts. Heartbeats and initialization metadata do not reset it. Successfully delivered text, reasoning, a tool call, or a successful terminal event ends the budget. There is no total generation timeout after model output begins.

Early heartbeats are opt-in. They cover synchronous executor startup and waiting for the first model output. They use the existing interval and flush `: keep-alive` SSE comments. This covers Chat Completions, legacy Completions, Responses, Claude Messages, and Gemini `streamGenerateContent` SSE. Gemini non-SSE streaming receives no SSE comments. WebSockets, image/speech endpoints, and non-streaming requests are unchanged.

An early heartbeat commits HTTP 200 and SSE headers. Errors after that point are protocol error events, including a first-output timeout; they cannot change the HTTP status to 504. Without a committed response a timeout returns HTTP 504. Internal completion and usage failure records preserve the gateway timeout instead of classifying it as a 499 client cancellation. Error-only request logging also records failures after HTTP 200. A failed stream does not emit a successful completion marker.

Headers obtained only after the first heartbeat cannot be sent as HTTP headers. Authentication/bootstrap retries still run before model output; heartbeat bytes do not consume tokens, count toward model TTFT, or prevent eligible account fallback. Client disconnects and heartbeat write/flush failures cancel upstream execution.

Heartbeats protect the CPA HTTP leg only when intermediate proxies forward and flush SSE. They do not control another proxy leg, WebSocket liveness, client timeouts, upstream idle policies, or a model output-token limit.
