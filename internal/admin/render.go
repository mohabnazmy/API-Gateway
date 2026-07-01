package admin

import (
	"bytes"
	"embed"
	"html/template"
	"io/fs"
	"net/http"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

//go:embed web/templates/*.gohtml web/static/*
var webFS embed.FS

// templates is parsed once at package init; a parse error is a programming bug
// and panics rather than failing per request.
var templates = template.Must(template.ParseFS(webFS, "web/templates/*.gohtml"))

// staticFS serves the vendored assets (htmx) under /admin/static/.
func staticFS() http.Handler {
	sub, err := fs.Sub(webFS, "web/static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(sub))
}

// pageData is the view model shared by every admin page. Common fields (User,
// CSRF) feed the layout; the rest are page-specific.
type pageData struct {
	User  string
	CSRF  string
	Error string

	Routes []model.Route
	Route  model.Route

	Plans []model.Plan
	Plan  model.Plan

	Consumers []model.Consumer
	Consumer  model.Consumer
	PlanNames map[int64]string // plan id → name, for rendering a consumer's tier

	Keys   []model.APIKey
	NewKey string // plaintext of a just-issued key, shown exactly once

	Stats stats // dashboard summary counts
}

// stats are the control-plane object counts shown on the dashboard.
type stats struct {
	Routes    int
	Plans     int
	Consumers int
}

// render writes a named template. It buffers first so a template error yields a
// 500 rather than a half-written page.
func (s *Service) render(w http.ResponseWriter, status int, name string, data pageData) {
	var buf bytes.Buffer
	if err := templates.ExecuteTemplate(&buf, name, data); err != nil {
		s.logger.Error("template render failed", "template", name, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = buf.WriteTo(w)
}
