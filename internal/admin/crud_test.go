package admin

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRouteCRUD(t *testing.T) {
	s, _, rl := newTestService(t, 30*time.Minute)
	h := s.Router()
	tok := login(t, h)
	body := `{"name":"users","path_prefix":"/api/users","upstream":"http://localhost:9001","strip_prefix":true}`

	if code := do(h, "POST", "/admin/api/routes", body, tok); code != http.StatusCreated {
		t.Fatalf("create = %d, want 201", code)
	}
	if rl.calls == 0 {
		t.Fatal("create did not trigger hot-reload")
	}
	if code := do(h, "POST", "/admin/api/routes", body, tok); code != http.StatusConflict {
		t.Fatalf("duplicate create = %d, want 409", code)
	}
	if rec := doRec(h, "GET", "/admin/api/routes", "", tok); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "users") {
		t.Fatalf("list routes: %d %s", rec.Code, rec.Body.String())
	}
	if code := do(h, "GET", "/admin/api/routes/users", "", tok); code != http.StatusOK {
		t.Fatalf("get = %d, want 200", code)
	}
	if code := do(h, "GET", "/admin/api/routes/ghost", "", tok); code != http.StatusNotFound {
		t.Fatalf("get unknown = %d, want 404", code)
	}
	upd := `{"path_prefix":"/api/users","upstream":"http://localhost:9999"}`
	if code := do(h, "PUT", "/admin/api/routes/users", upd, tok); code != http.StatusOK {
		t.Fatalf("put = %d, want 200", code)
	}
	if code := do(h, "PUT", "/admin/api/routes/ghost", upd, tok); code != http.StatusNotFound {
		t.Fatalf("put unknown = %d, want 404", code)
	}
	if code := do(h, "DELETE", "/admin/api/routes/users", "", tok); code != http.StatusNoContent {
		t.Fatalf("delete = %d, want 204", code)
	}
	if code := do(h, "DELETE", "/admin/api/routes/users", "", tok); code != http.StatusNotFound {
		t.Fatalf("delete again = %d, want 404", code)
	}
}

func TestRouteValidation(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	h := s.Router()
	tok := login(t, h)
	cases := []string{
		`{"name":"bad","path_prefix":"noslash","upstream":"http://up"}`,
		`{"name":"bad","path_prefix":"/x","upstream":"not-a-url"}`,
		`{"name":"","path_prefix":"/x","upstream":"http://up"}`,
	}
	for _, c := range cases {
		if code := do(h, "POST", "/admin/api/routes", c, tok); code != http.StatusUnprocessableEntity {
			t.Fatalf("invalid route %s -> %d, want 422", c, code)
		}
	}
}

func TestPlanCRUD(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	h := s.Router()
	tok := login(t, h)

	rec := doRec(h, "POST", "/admin/api/plans", `{"name":"pro","rps":500,"burst":1000}`, tok)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"id"`) {
		t.Fatalf("create plan: %d %s", rec.Code, rec.Body.String())
	}
	if code := do(h, "POST", "/admin/api/plans", `{"name":"bad","rps":0,"burst":10}`, tok); code != http.StatusUnprocessableEntity {
		t.Fatalf("rps<=0 -> %d, want 422", code)
	}
	if code := do(h, "PUT", "/admin/api/plans/1", `{"name":"pro","rps":600,"burst":1000}`, tok); code != http.StatusOK {
		t.Fatalf("put plan = %d, want 200", code)
	}
	if code := do(h, "PUT", "/admin/api/plans/999", `{"name":"x","rps":1,"burst":1}`, tok); code != http.StatusNotFound {
		t.Fatalf("put unknown plan = %d, want 404", code)
	}
	if code := do(h, "DELETE", "/admin/api/plans/1", "", tok); code != http.StatusNoContent {
		t.Fatalf("delete plan = %d, want 204", code)
	}
}

func TestConsumerCRUDWithPlanRef(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	h := s.Router()
	tok := login(t, h)

	// Consumer referencing a nonexistent plan → 422.
	if code := do(h, "POST", "/admin/api/consumers", `{"name":"acme","plan_id":42,"enabled":true}`, tok); code != http.StatusUnprocessableEntity {
		t.Fatalf("consumer w/ bad plan = %d, want 422", code)
	}
	// Create a plan, then a consumer referencing it.
	_ = do(h, "POST", "/admin/api/plans", `{"name":"free","rps":60,"burst":60}`, tok)
	rec := doRec(h, "POST", "/admin/api/consumers", `{"name":"acme","plan_id":1,"enabled":true}`, tok)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create consumer = %d %s", rec.Code, rec.Body.String())
	}
	if code := do(h, "GET", "/admin/api/consumers/1", "", tok); code != http.StatusOK {
		t.Fatalf("get consumer = %d, want 200", code)
	}
	if code := do(h, "PUT", "/admin/api/consumers/1", `{"name":"acme-corp","enabled":false}`, tok); code != http.StatusOK {
		t.Fatalf("put consumer = %d, want 200", code)
	}
	if code := do(h, "GET", "/admin/api/consumers/999", "", tok); code != http.StatusNotFound {
		t.Fatalf("get unknown consumer = %d, want 404", code)
	}
}

func TestAPIKeyEndpoints(t *testing.T) {
	s, _, _ := newTestService(t, 30*time.Minute)
	h := s.Router()
	tok := login(t, h)
	_ = do(h, "POST", "/admin/api/consumers", `{"name":"acme","enabled":true}`, tok)

	// Issue a key → 201, returns the plaintext once (gwk_ prefix), never a hash.
	rec := doRec(h, "POST", "/admin/api/consumers/1/api-keys", `{"name":"prod"}`, tok)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create key = %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"key":"gwk_`) {
		t.Fatalf("create key did not return plaintext: %s", body)
	}
	if strings.Contains(body, "hash") {
		t.Fatalf("response leaked a hash: %s", body)
	}

	// List returns metadata only (no key/hash).
	listRec := doRec(h, "GET", "/admin/api/consumers/1/api-keys", "", tok)
	if listRec.Code != http.StatusOK || strings.Contains(listRec.Body.String(), "gwk_") || strings.Contains(listRec.Body.String(), "hash") {
		t.Fatalf("list keys leaked secret: %d %s", listRec.Code, listRec.Body.String())
	}

	// Key for a missing consumer → 404.
	if code := do(h, "POST", "/admin/api/consumers/999/api-keys", `{"name":"x"}`, tok); code != http.StatusNotFound {
		t.Fatalf("key for unknown consumer = %d, want 404", code)
	}
	// Revoke, then revoke again → 404.
	if code := do(h, "DELETE", "/admin/api/api-keys/1", "", tok); code != http.StatusNoContent {
		t.Fatalf("revoke = %d, want 204", code)
	}
	if code := do(h, "DELETE", "/admin/api/api-keys/1", "", tok); code != http.StatusNotFound {
		t.Fatalf("revoke again = %d, want 404", code)
	}
}
