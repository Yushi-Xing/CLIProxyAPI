package helps

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

type fixtureStartupTimeout struct{}

func (fixtureStartupTimeout) Error() string   { return "fixture first output timeout" }
func (fixtureStartupTimeout) StatusCode() int { return http.StatusGatewayTimeout }
func (fixtureStartupTimeout) Unwrap() error   { return context.DeadlineExceeded }

func TestUsageStartupTimeoutPreservesCancelCause(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(fixtureStartupTimeout{})
	got := failFromContextErrors(ctx, context.Canceled)
	if got.StatusCode != 504 || got.Body != "fixture first output timeout" {
		t.Fatalf("timeout=%+v", got)
	}
	// An independent upstream request fault must retain its own failure.
	got = failFromContextErrors(ctx, errors.New("upstream request fault"))
	if got.Body != "upstream request fault" {
		t.Fatalf("request fault=%+v", got)
	}
	ctx, cancel = context.WithCancelCause(context.Background())
	cancel(context.Canceled)
	if got := failFromContextErrors(ctx, context.Canceled); got.StatusCode != 499 {
		t.Fatalf("disconnect=%+v", got)
	}
}
