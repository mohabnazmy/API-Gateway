package admin

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestUIDashboard(t *testing.T) {
	ts, _ := newUIServer(t)
	c, csrf := loginUI(t, ts)

	// Empty control plane → all counts zero, page renders.
	page, _ := c.Get(ts.URL + "/admin")
	if b := body(t, page); page.StatusCode != http.StatusOK || !strings.Contains(b, "Dashboard") {
		t.Fatalf("dashboard did not render: %d", page.StatusCode)
	}

	// Add one of each; the dashboard reflects the new counts.
	_ = mutate(t, c, "POST", ts.URL+"/admin/routes", csrf,
		url.Values{"name": {"api"}, "path_prefix": {"/api"}, "upstream": {"http://up"}})
	_ = mutate(t, c, "POST", ts.URL+"/admin/plans", csrf,
		url.Values{"name": {"free"}, "rps": {"60"}, "burst": {"60"}})
	_ = mutate(t, c, "POST", ts.URL+"/admin/consumers", csrf,
		url.Values{"name": {"acme"}, "enabled": {"true"}})

	page2, _ := c.Get(ts.URL + "/admin")
	b := body(t, page2)
	// Each count should now read 1 (rendered inside a .card as the big number).
	for _, label := range []string{"Routes", "Plans", "Consumers"} {
		if !strings.Contains(b, label) {
			t.Fatalf("dashboard missing %s card", label)
		}
	}
	if strings.Count(b, `class="n">1<`) != 3 {
		t.Fatalf("expected three counts of 1, body:\n%s", b)
	}
}
