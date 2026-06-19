package registry

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// A failed Load must leave the previously active snapshot intact (atomicity, N5).
func TestFailedLoadKeepsPreviousSnapshot(t *testing.T) {
	reg := New(discardLogger())
	good := []model.Route{{Name: "g", PathPrefix: "/g", Upstream: "http://example:1"}}
	if err := reg.Load(good); err != nil {
		t.Fatal(err)
	}
	before := reg.Current()

	bad := []model.Route{{Name: "b", PathPrefix: "/b", Upstream: "://bad url with space"}}
	if err := reg.Load(bad); err == nil {
		t.Fatal("expected Load to fail on an invalid upstream")
	}
	if reg.Current() != before {
		t.Fatal("snapshot was swapped despite a failed Load")
	}
	if _, ok := reg.Current().Match(httptest.NewRequest("GET", "/g/x", nil)); !ok {
		t.Fatal("the good route was lost after a failed Load")
	}
}

func TestUnknownAlgorithmFailsLoad(t *testing.T) {
	reg := New(discardLogger())
	err := reg.Load([]model.Route{{
		Name: "a", PathPrefix: "/a", Upstream: "http://example:1",
		RateLimit: model.RateLimitPolicy{Algorithm: "does-not-exist", RPS: 1, Burst: 1},
	}})
	if err == nil {
		t.Fatal("expected Load to fail for an unknown rate-limit algorithm")
	}
}

// Concurrent readers + repeated reloads. Run with -race to verify the atomic
// snapshot swap is free of data races (N1, N5).
func TestConcurrentReadsAndReloads(t *testing.T) {
	reg := New(discardLogger())
	routes := []model.Route{{
		Name: "r", PathPrefix: "/r", Upstream: "http://example:1",
		RateLimit: model.RateLimitPolicy{Algorithm: "token_bucket", RPS: 100, Burst: 100},
	}}
	if err := reg.Load(routes); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest("GET", "/r/x", nil)
			for {
				select {
				case <-stop:
					return
				default:
					reg.Current().Match(req)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			if err := reg.Load(routes); err != nil {
				t.Errorf("reload failed: %v", err)
			}
		}
		close(stop)
	}()
	wg.Wait()
}
