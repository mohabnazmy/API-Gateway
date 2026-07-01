package admin

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// csrfCookie holds the double-submit CSRF token. It is readable by the page (not
// HttpOnly) so forms/HTMX can echo it back; a cross-site page can't read it, so a
// forged request can't supply a matching token.
const csrfCookie = "csrf_token"

// webAuth guards the HTML UI: it authenticates via cookie-or-Bearer (redirecting
// to the login page on failure, not a JSON 401) and enforces CSRF on mutations.
func (s *Service) webAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, err := s.verify(r.Context(), s.tokenFromRequest(r))
		if err != nil {
			http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
			return
		}
		if isMutating(r.Method) && !validCSRF(r) {
			http.Error(w, "invalid or missing CSRF token", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey, user)))
	})
}

func (s *Service) loginPage(w http.ResponseWriter, _ *http.Request) {
	s.render(w, http.StatusOK, "login", pageData{})
}

func (s *Service) loginSubmit(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	token, err := s.Login(r.Context(), r.FormValue("username"), r.FormValue("password"))
	if err != nil {
		s.render(w, http.StatusUnauthorized, "login", pageData{Error: "invalid credentials"})
		return
	}
	s.setSession(w, r, token)
	http.Redirect(w, r, "/admin/routes", http.StatusSeeOther)
}

func (s *Service) logout(w http.ResponseWriter, r *http.Request) {
	clearCookie(w, sessionCookie)
	clearCookie(w, csrfCookie)
	http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
}

func (s *Service) routesPage(w http.ResponseWriter, r *http.Request) {
	routes, err := s.store.ListRoutes(r.Context())
	if err != nil {
		s.serverError(w, "list routes", err)
		return
	}
	s.render(w, http.StatusOK, "routes", s.page(r, pageData{Routes: routes}))
}

func (s *Service) createRouteUI(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	rt := routeFromForm(r, "")
	if msg := validateRoute(rt); msg != "" {
		s.renderRoutesFrag(w, r, msg)
		return
	}
	if _, exists, err := s.findRoute(r.Context(), rt.Name); err != nil {
		s.serverError(w, "create route", err)
		return
	} else if exists {
		s.renderRoutesFrag(w, r, "a route named "+rt.Name+" already exists")
		return
	}
	if err := s.store.UpsertRoute(r.Context(), rt); err != nil {
		s.serverError(w, "create route", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderRoutesFrag(w, r, "")
}

func (s *Service) editRoutePage(w http.ResponseWriter, r *http.Request) {
	rt, ok, err := s.findRoute(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.serverError(w, "edit route", err)
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "route_edit", s.page(r, pageData{Route: rt}))
}

func (s *Service) updateRouteUI(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	_ = r.ParseForm()
	rt := routeFromForm(r, name)
	if msg := validateRoute(rt); msg != "" {
		s.render(w, http.StatusUnprocessableEntity, "route_edit", s.page(r, pageData{Route: rt, Error: msg}))
		return
	}
	if _, exists, err := s.findRoute(r.Context(), name); err != nil {
		s.serverError(w, "update route", err)
		return
	} else if !exists {
		http.NotFound(w, r)
		return
	}
	if err := s.store.UpsertRoute(r.Context(), rt); err != nil {
		s.serverError(w, "update route", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	// HTMX navigates the browser to the list on success.
	w.Header().Set("HX-Redirect", "/admin/routes")
	w.WriteHeader(http.StatusOK)
}

func (s *Service) deleteRouteUI(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.DeleteRoute(r.Context(), chi.URLParam(r, "name")); err != nil {
		s.serverError(w, "delete route", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderRoutesFrag(w, r, "")
}

// renderRoutesFrag returns the #routes list fragment (with an optional error) for
// HTMX to swap in.
func (s *Service) renderRoutesFrag(w http.ResponseWriter, r *http.Request, errMsg string) {
	routes, err := s.store.ListRoutes(r.Context())
	if err != nil {
		s.serverError(w, "list routes", err)
		return
	}
	s.render(w, http.StatusOK, "routes_frag", s.page(r, pageData{Routes: routes, Error: errMsg}))
}

// --- helpers ---

// page fills the layout-common fields (current user, CSRF token) onto a pageData.
func (s *Service) page(r *http.Request, d pageData) pageData {
	d.User, _ = AdminUserFromContext(r.Context())
	d.CSRF = csrfValue(r)
	return d
}

func routeFromForm(r *http.Request, name string) model.Route {
	if name == "" {
		name = strings.TrimSpace(r.FormValue("name"))
	}
	return model.Route{
		Name:        name,
		PathPrefix:  strings.TrimSpace(r.FormValue("path_prefix")),
		Upstream:    strings.TrimSpace(r.FormValue("upstream")),
		StripPrefix: r.FormValue("strip_prefix") == "true",
		Auth:        model.AuthPolicy{RequireAuth: r.FormValue("require_auth") == "true"},
	}
}

func (s *Service) setSession(w http.ResponseWriter, r *http.Request, token string) {
	secure := r.TLS != nil // Secure only over HTTPS, so cookies still work on localhost HTTP
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/admin",
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name: csrfCookie, Value: randomToken(), Path: "/admin",
		Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/admin", MaxAge: -1})
}

func csrfValue(r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil {
		return c.Value
	}
	return ""
}

// validCSRF implements the double-submit check: the token sent in the header (or
// form field) must equal the CSRF cookie.
func validCSRF(r *http.Request) bool {
	c, err := r.Cookie(csrfCookie)
	if err != nil || c.Value == "" {
		return false
	}
	got := r.Header.Get("X-CSRF-Token")
	if got == "" {
		_ = r.ParseForm()
		got = r.FormValue("csrf_token")
	}
	return got != "" && subtle.ConstantTimeCompare([]byte(got), []byte(c.Value)) == 1
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
