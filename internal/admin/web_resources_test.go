package admin

import (
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// mutate performs a CSRF-protected form request with the logged-in client.
func mutate(t *testing.T, c *http.Client, method, url, csrf string, form url.Values) *http.Response {
	t.Helper()
	var bodyR io.Reader
	if form != nil {
		bodyR = strings.NewReader(form.Encode())
	}
	req, _ := http.NewRequest(method, url, bodyR)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestUIPlanCRUD(t *testing.T) {
	ts, _ := newUIServer(t)
	c, csrf := loginUI(t, ts)

	resp := mutate(t, c, "POST", ts.URL+"/admin/plans", csrf,
		url.Values{"name": {"pro"}, "rps": {"500"}, "burst": {"1000"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "pro") {
		t.Fatal("create plan did not render the new plan")
	}
	if page, _ := c.Get(ts.URL + "/admin/plans"); !strings.Contains(body(t, page), "pro") {
		t.Fatal("plan not shown on list page")
	}
	// Non-numeric rps → inline validation error.
	bad := mutate(t, c, "POST", ts.URL+"/admin/plans", csrf,
		url.Values{"name": {"bad"}, "rps": {"abc"}, "burst": {"1"}})
	if !strings.Contains(body(t, bad), "rps must be a number") {
		t.Fatal("expected rps validation error")
	}
	// Delete (the "pro" plan is id 1).
	if del := mutate(t, c, "DELETE", ts.URL+"/admin/plans/1", csrf, nil); del.StatusCode != http.StatusOK {
		t.Fatalf("delete plan = %d", del.StatusCode)
	}
}

func TestUIConsumerAndKeysFlow(t *testing.T) {
	ts, _ := newUIServer(t)
	c, csrf := loginUI(t, ts)

	// A consumer referencing a nonexistent plan → inline error.
	badC := mutate(t, c, "POST", ts.URL+"/admin/consumers", csrf,
		url.Values{"name": {"x"}, "plan_id": {"99"}, "enabled": {"true"}})
	if !strings.Contains(body(t, badC), "does not exist") {
		t.Fatal("expected plan-not-found error")
	}

	// Create a plan, then a consumer on it.
	_ = mutate(t, c, "POST", ts.URL+"/admin/plans", csrf, url.Values{"name": {"free"}, "rps": {"60"}, "burst": {"60"}})
	resp := mutate(t, c, "POST", ts.URL+"/admin/consumers", csrf,
		url.Values{"name": {"acme"}, "plan_id": {"1"}, "enabled": {"true"}})
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "acme") {
		t.Fatal("create consumer failed")
	}

	// Edit page shows the consumer + its (empty) key list.
	edit, _ := c.Get(ts.URL + "/admin/consumers/1/edit")
	if b := body(t, edit); !strings.Contains(b, "acme") || !strings.Contains(b, "API keys") {
		t.Fatal("consumer edit page missing consumer/keys section")
	}

	// Issue a key → the plaintext (gwk_) is shown once, with its label.
	issued := mutate(t, c, "POST", ts.URL+"/admin/consumers/1/api-keys", csrf, url.Values{"name": {"prod"}})
	ib := body(t, issued)
	if !strings.Contains(ib, "gwk_") || !strings.Contains(ib, "prod") || !strings.Contains(ib, "shown only once") {
		t.Fatalf("issue key response wrong: %s", ib)
	}

	// Revoke it (key id 1, consumer 1) → the row is no longer active.
	rev := mutate(t, c, "DELETE", ts.URL+"/admin/api-keys/1?consumer=1", csrf, nil)
	if rb := body(t, rev); rev.StatusCode != http.StatusOK || !strings.Contains(rb, "revoked") {
		t.Fatalf("revoke key: %d %s", rev.StatusCode, rb)
	}
}
