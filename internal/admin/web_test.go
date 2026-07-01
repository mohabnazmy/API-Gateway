package admin

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func newUIServer(t *testing.T) (*httptest.Server, *fakeReloader) {
	t.Helper()
	s, _, rl := newTestService(t, 30*time.Minute)
	ts := httptest.NewServer(s.Router())
	t.Cleanup(ts.Close)
	return ts, rl
}

// loginUI logs in with root/secret and returns a cookie-jar client plus the CSRF
// token to echo on mutations.
func loginUI(t *testing.T, ts *httptest.Server) (*http.Client, string) {
	t.Helper()
	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	resp, err := c.PostForm(ts.URL+"/admin/login", url.Values{"username": {"root"}, "password": {"secret"}})
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK { // followed 303 → /admin/routes
		t.Fatalf("login landed on %d, want 200", resp.StatusCode)
	}
	u, _ := url.Parse(ts.URL + "/admin/routes")
	var csrf string
	for _, ck := range jar.Cookies(u) {
		if ck.Name == csrfCookie {
			csrf = ck.Value
		}
	}
	if csrf == "" {
		t.Fatal("no CSRF cookie after login")
	}
	return c, csrf
}

func body(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(b)
}

func TestLoginPageRenders(t *testing.T) {
	ts, _ := newUIServer(t)
	resp, err := http.Get(ts.URL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "Admin login") {
		t.Fatal("login page did not render")
	}
}

func TestUIRequiresAuth(t *testing.T) {
	ts, _ := newUIServer(t)
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(ts.URL + "/admin/routes")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/admin/login" {
		t.Fatalf("unauth /admin/routes = %d loc=%q, want 303 -> /admin/login", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestLoginRejectsBadPassword(t *testing.T) {
	ts, _ := newUIServer(t)
	resp, err := http.PostForm(ts.URL+"/admin/login", url.Values{"username": {"root"}, "password": {"nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body(t, resp), "invalid credentials") {
		t.Fatalf("bad login = %d", resp.StatusCode)
	}
}

func TestUIRouteCreateAndDelete(t *testing.T) {
	ts, rl := newUIServer(t)
	c, csrf := loginUI(t, ts)

	// Create a route via the HTML form (HTMX POST).
	form := url.Values{"name": {"api"}, "path_prefix": {"/api"}, "upstream": {"http://localhost:9001"}, "strip_prefix": {"true"}}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/routes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "/api") {
		t.Fatalf("create route = %d", resp.StatusCode)
	}
	if rl.calls == 0 {
		t.Fatal("UI create did not trigger hot-reload")
	}

	// The list page now shows it.
	page, _ := c.Get(ts.URL + "/admin/routes")
	if !strings.Contains(body(t, page), "api") {
		t.Fatal("route not shown on list page")
	}

	// Delete it.
	del, _ := http.NewRequest("DELETE", ts.URL+"/admin/routes/api", nil)
	del.Header.Set("X-CSRF-Token", csrf)
	dresp, err := c.Do(del)
	if err != nil {
		t.Fatal(err)
	}
	if dresp.StatusCode != http.StatusOK || strings.Contains(body(t, dresp), ">api<") {
		t.Fatalf("delete route = %d (still present?)", dresp.StatusCode)
	}
}

func TestUIMutationRequiresCSRF(t *testing.T) {
	ts, _ := newUIServer(t)
	c, _ := loginUI(t, ts)
	// Authenticated (cookie present) but no CSRF token → 403.
	form := url.Values{"name": {"x"}, "path_prefix": {"/x"}, "upstream": {"http://up"}}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/routes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without CSRF = %d, want 403", resp.StatusCode)
	}
}

func TestUICreateValidationError(t *testing.T) {
	ts, _ := newUIServer(t)
	c, csrf := loginUI(t, ts)
	form := url.Values{"name": {"bad"}, "path_prefix": {"noslash"}, "upstream": {"http://up"}}
	req, _ := http.NewRequest("POST", ts.URL+"/admin/routes", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", csrf)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(body(t, resp), "path_prefix") {
		t.Fatalf("expected inline validation error, got %d", resp.StatusCode)
	}
}
