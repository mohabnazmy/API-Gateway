package configsync

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

type fakeSource struct {
	version int64
	routes  []model.Route
	listErr error
	lists   int
}

func (f *fakeSource) Version(ctx context.Context) (int64, error) { return f.version, nil }
func (f *fakeSource) ListRoutes(ctx context.Context) ([]model.Route, error) {
	f.lists++
	return f.routes, f.listErr
}

type fakeTarget struct {
	loaded  [][]model.Route
	loadErr error
}

func (t *fakeTarget) Load(routes []model.Route) error {
	if t.loadErr != nil {
		return t.loadErr
	}
	t.loaded = append(t.loaded, routes)
	return nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func routes(names ...string) []model.Route {
	out := make([]model.Route, len(names))
	for i, n := range names {
		out[i] = model.Route{Name: n, PathPrefix: "/" + n, Upstream: "http://up"}
	}
	return out
}

func TestLoadNowLoadsAndRecordsVersion(t *testing.T) {
	src := &fakeSource{version: 7, routes: routes("a")}
	tgt := &fakeTarget{}
	r := New(src, tgt, quietLogger())

	if err := r.LoadNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(tgt.loaded) != 1 || len(tgt.loaded[0]) != 1 {
		t.Fatalf("expected initial load of 1 route, got %v", tgt.loaded)
	}
	// A subsequent poll at the same version must NOT reload.
	reloaded, err := r.pollOnce(context.Background())
	if err != nil || reloaded {
		t.Fatalf("poll at unchanged version reloaded=%v err=%v", reloaded, err)
	}
}

func TestPollReloadsOnVersionChange(t *testing.T) {
	src := &fakeSource{version: 1, routes: routes("a")}
	tgt := &fakeTarget{}
	r := New(src, tgt, quietLogger())
	if err := r.LoadNow(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Config changes: new version + routes.
	src.version = 2
	src.routes = routes("a", "b")
	reloaded, err := r.pollOnce(context.Background())
	if err != nil || !reloaded {
		t.Fatalf("expected reload on version bump, reloaded=%v err=%v", reloaded, err)
	}
	last := tgt.loaded[len(tgt.loaded)-1]
	if len(last) != 2 {
		t.Fatalf("reloaded snapshot has %d routes, want 2", len(last))
	}
}

func TestFailedLoadDoesNotAdvanceVersion(t *testing.T) {
	src := &fakeSource{version: 1, routes: routes("a")}
	tgt := &fakeTarget{}
	r := New(src, tgt, quietLogger())
	_ = r.LoadNow(context.Background())

	// Version bumps but the target fails to apply.
	src.version = 2
	tgt.loadErr = errors.New("compile failed")
	if _, err := r.pollOnce(context.Background()); err == nil {
		t.Fatal("expected error from failed load")
	}

	// Recovery: the next poll must retry (version not advanced past the failure).
	tgt.loadErr = nil
	reloaded, err := r.pollOnce(context.Background())
	if err != nil || !reloaded {
		t.Fatalf("expected retry to reload after recovery, reloaded=%v err=%v", reloaded, err)
	}
}
