package openai

import "testing"

func TestResponsesLocalInterruptRequiresCurrentResponse(t *testing.T) {
	local := newResponsesLocalInterrupt()
	local.begin(false)
	current := []byte(`{"type":"response.interrupt","response_id":"current"}`)
	if local.deliver(current) {
		t.Fatal("accepted interrupt before a response ID was announced")
	}
	local.observe([]byte(`{"type":"response.created","response":{"id":"current"}}`))
	for _, payload := range []string{
		`{"type":"response.interrupt","response_id":"unknown"}`,
		`{"type":"response.interrupt","response_id":"previous"}`,
		`{"type":"response.interrupt","response_id":42}`,
	} {
		if local.deliver([]byte(payload)) {
			t.Fatalf("accepted unrelated interrupt: %s", payload)
		}
	}
	if !local.deliver(current) {
		t.Fatal("rejected interrupt for the active response")
	}
	for _, event := range []string{"response.completed", "response.done", "response.incomplete", "response.failed", "error"} {
		local.observe([]byte(`{"type":"` + event + `","response":{"id":"current"}}`))
		if local.deliver(current) {
			t.Fatalf("accepted interrupt after %s", event)
		}
		select {
		case <-local.framesChan():
			// A claimed interrupt remains queued until the forwarder answers it.
		default:
		}
		// A retained duplex stream can start another response without begin().
		local.observe([]byte(`{"type":"response.created","response":{"id":"current"}}`))
		if !local.deliver(current) {
			t.Fatalf("next response not interruptible after %s", event)
		}
	}
	local.end()
	if local.deliver(current) {
		t.Fatal("accepted interrupt after forwarding ended")
	}
}
