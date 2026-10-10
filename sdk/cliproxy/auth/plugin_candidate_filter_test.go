package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const candidateFilterModel = "candidate-filter-model"

type candidateFilterFixture struct {
	manager   *Manager
	scheduler *fakePluginScheduler
	allowed   map[string]bool
	mixed     bool
}

func newCandidateFilterFixture(t *testing.T, selector Selector, mixed bool) *candidateFilterFixture {
	t.Helper()
	ctx := WithSkipPersist(context.Background())
	manager := NewManager(nil, selector, nil)
	manager.RegisterExecutor(schedulerTestExecutor{provider: "zed"})
	manager.RegisterExecutor(schedulerTestExecutor{provider: "antigravity"})
	outsideProvider := "zed"
	if mixed {
		outsideProvider = "antigravity"
	}
	for _, auth := range []*Auth{
		{ID: "filter-outside", Provider: outsideProvider, Attributes: map[string]string{"priority": "100"}},
		{ID: "filter-a", Provider: "zed", Attributes: map[string]string{"priority": "3", "weight": "2"}},
		{ID: "filter-b", Provider: "zed", Attributes: map[string]string{"priority": "3", "weight": "1"}},
		{ID: "filter-low", Provider: "zed", Attributes: map[string]string{"priority": "0"}},
	} {
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatal(err)
		}
		registerSchedulerModels(t, auth.Provider, candidateFilterModel, auth.ID)
	}
	fixture := &candidateFilterFixture{manager: manager, mixed: mixed, allowed: map[string]bool{"filter-a": true, "filter-b": true, "filter-low": true}}
	fixture.scheduler = &fakePluginScheduler{acrossPriorities: true, pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
		if !req.SupportsCandidateFiltering {
			t.Fatal("host did not advertise candidate filtering")
		}
		ids := make([]string, 0, len(req.Candidates))
		for _, candidate := range req.Candidates {
			if fixture.allowed[candidate.ID] {
				ids = append(ids, candidate.ID)
			}
		}
		return pluginapi.SchedulerPickResponse{Handled: true, AllowedAuthIDs: ids}, true, nil
	}}
	manager.SetPluginScheduler(fixture.scheduler)
	return fixture
}

func (f *candidateFilterFixture) pick(opts cliproxyexecutor.Options, tried map[string]struct{}) (*Auth, error) {
	if f.mixed {
		auth, _, _, err := f.manager.pickNextMixed(context.Background(), []string{"zed", "antigravity"}, candidateFilterModel, opts, tried)
		return auth, err
	}
	auth, _, err := f.manager.pickNext(context.Background(), "zed", candidateFilterModel, opts, tried)
	return auth, err
}

func TestPluginCandidateFilterUsesConfiguredStrategy(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, strategy := range []string{"fill-first", "round-robin", "weighted-round-robin"} {
			name := strategy + "/single"
			if mixed {
				name = strategy + "/mixed"
			}
			t.Run(name, func(t *testing.T) {
				var selector Selector = &FillFirstSelector{}
				if strategy == "round-robin" {
					selector = &RoundRobinSelector{}
				}
				if strategy == "weighted-round-robin" {
					selector = &WeightedRoundRobinSelector{}
				}
				fixture := newCandidateFilterFixture(t, selector, mixed)
				counts := map[string]int{}
				for i := 0; i < 12; i++ {
					auth, err := fixture.pick(cliproxyexecutor.Options{}, nil)
					if err != nil {
						t.Fatal(err)
					}
					counts[auth.ID]++
				}
				want := map[string]int{"filter-a": 12}
				if strategy == "round-robin" {
					want = map[string]int{"filter-a": 6, "filter-b": 6}
				}
				if strategy == "weighted-round-robin" {
					want = map[string]int{"filter-a": 8, "filter-b": 4}
				}
				if !reflect.DeepEqual(counts, want) {
					t.Fatalf("picks=%v, want %v", counts, want)
				}
			})
		}
	}
}

func TestPluginCandidateFilterAffinityFailoverRecoveryAndRevocation(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		name := "single"
		if mixed {
			name = "mixed"
		}
		t.Run(name, func(t *testing.T) {
			affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &FillFirstSelector{}, TTL: time.Hour})
			defer affinity.Stop()
			fixture := newCandidateFilterFixture(t, affinity, mixed)
			opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "stable-filter-session"}}
			pick := func(want string) {
				t.Helper()
				auth, err := fixture.pick(opts, nil)
				if err != nil || auth == nil || auth.ID != want {
					t.Fatalf("pick=%+v, err=%v; want %s", auth, err, want)
				}
			}
			pick("filter-a")
			pick("filter-a")
			fixture.manager.MarkResult(context.Background(), Result{AuthID: "filter-a", Model: candidateFilterModel, Error: &Error{Code: "upstream_error", HTTPStatus: http.StatusServiceUnavailable}})
			pick("filter-b")
			fixture.manager.MarkResult(context.Background(), Result{AuthID: "filter-b", Model: candidateFilterModel, Error: &Error{Code: "upstream_error", HTTPStatus: http.StatusServiceUnavailable}})
			pick("filter-low")
			fixture.manager.MarkResult(context.Background(), Result{AuthID: "filter-a", Model: candidateFilterModel, Success: true})
			pick("filter-low")
			fresh := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.DerivedSessionIDMetadataKey: "new-filter-session"}}
			auth, err := fixture.pick(fresh, nil)
			if err != nil || auth == nil || auth.ID != "filter-a" {
				t.Fatalf("new session=%+v, err=%v", auth, err)
			}
			delete(fixture.allowed, "filter-low")
			pick("filter-a")
			fixture.allowed = map[string]bool{}
			auth, err = fixture.pick(opts, nil)
			var failure *Error
			if auth != nil || !errors.As(err, &failure) || failure.Code != "no_routed_credential" {
				t.Fatalf("revoked pool=%+v, err=%v", auth, err)
			}
		})
	}
}

func TestPluginCandidateFilterRetryNeverWidensPermissions(t *testing.T) {
	fixture := newCandidateFilterFixture(t, &FillFirstSelector{}, true)
	tried := map[string]struct{}{}
	for _, want := range []string{"filter-a", "filter-b", "filter-low"} {
		auth, err := fixture.pick(cliproxyexecutor.Options{}, tried)
		if err != nil || auth == nil || auth.ID != want {
			t.Fatalf("pick=%+v, err=%v; want %s", auth, err, want)
		}
		tried[auth.ID] = struct{}{}
	}
	auth, err := fixture.pick(cliproxyexecutor.Options{}, tried)
	if auth != nil || err == nil {
		t.Fatalf("exhausted authorized set=%+v, err=%v", auth, err)
	}
}

func TestPluginCandidateFilterInvalidResponsesFailClosed(t *testing.T) {
	for _, response := range []pluginapi.SchedulerPickResponse{
		{Handled: true, AllowedAuthIDs: []string{}},
		{Handled: true, AllowedAuthIDs: []string{"not-a-candidate"}},
		{Handled: true, AllowedAuthIDs: []string{""}},
		{Handled: false, AllowedAuthIDs: []string{"filter-a"}},
		{Handled: true, AllowedAuthIDs: []string{"filter-a"}, AuthID: "filter-outside"},
		{Handled: true, AllowedAuthIDs: []string{"filter-a"}, DelegateBuiltin: pluginapi.SchedulerBuiltinRoundRobin},
	} {
		fixture := newCandidateFilterFixture(t, &FillFirstSelector{}, false)
		fixture.scheduler.pick = nil
		fixture.scheduler.handled = true
		fixture.scheduler.resp = response
		auth, err := fixture.pick(cliproxyexecutor.Options{}, nil)
		if auth != nil || err == nil {
			t.Fatalf("response=%+v selected=%+v, err=%v", response, auth, err)
		}
	}
}

func TestPluginCandidateFilterWeightedPrioritySkipsZeroWeight(t *testing.T) {
	fixture := newCandidateFilterFixture(t, &WeightedRoundRobinSelector{}, false)
	fixture.allowed["filter-zero"] = true
	if _, err := fixture.manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "filter-zero", Provider: "zed", Attributes: map[string]string{"priority": "10", "weight": "0"}}); err != nil {
		t.Fatal(err)
	}
	registerSchedulerModels(t, "zed", candidateFilterModel, "filter-zero")
	auth, err := fixture.pick(cliproxyexecutor.Options{}, nil)
	if err != nil || auth == nil || auth.ID != "filter-a" {
		t.Fatalf("pick=%+v, err=%v", auth, err)
	}
}
