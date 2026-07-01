package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Router builds the admin API router: public health + login, and everything else
// guarded by the session-token middleware.
func (s *Service) Router() http.Handler {
	r := chi.NewRouter()

	// --- Admin UI (HTML) ---
	r.Get("/admin/login", s.loginPage)
	r.Post("/admin/login", s.loginSubmit)
	r.Handle("/admin/static/*", http.StripPrefix("/admin/static/", staticFS()))
	r.Group(func(r chi.Router) {
		r.Use(s.webAuth)
		r.Get("/admin", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/admin/routes", http.StatusSeeOther)
		})
		r.Post("/admin/logout", s.logout)
		r.Get("/admin/routes", s.routesPage)
		r.Post("/admin/routes", s.createRouteUI)
		r.Get("/admin/routes/{name}/edit", s.editRoutePage)
		r.Put("/admin/routes/{name}", s.updateRouteUI)
		r.Delete("/admin/routes/{name}", s.deleteRouteUI)

		r.Get("/admin/plans", s.plansPage)
		r.Post("/admin/plans", s.createPlanUI)
		r.Get("/admin/plans/{id}/edit", s.editPlanPage)
		r.Put("/admin/plans/{id}", s.updatePlanUI)
		r.Delete("/admin/plans/{id}", s.deletePlanUI)

		r.Get("/admin/consumers", s.consumersPage)
		r.Post("/admin/consumers", s.createConsumerUI)
		r.Get("/admin/consumers/{id}/edit", s.editConsumerPage)
		r.Put("/admin/consumers/{id}", s.updateConsumerUI)
		r.Delete("/admin/consumers/{id}", s.deleteConsumerUI)
		r.Post("/admin/consumers/{id}/api-keys", s.createKeyUI)
		r.Delete("/admin/api-keys/{id}", s.revokeKeyUI)
	})

	// --- Admin API (JSON) ---
	r.Get("/admin/api/health", healthHandler)
	r.Post("/admin/api/auth/login", s.loginHandler)

	r.Group(func(r chi.Router) {
		r.Use(s.Middleware)
		r.Get("/admin/api/me", meHandler)

		r.Get("/admin/api/routes", s.listRoutes)
		r.Post("/admin/api/routes", s.createRoute)
		r.Get("/admin/api/routes/{name}", s.getRoute)
		r.Put("/admin/api/routes/{name}", s.putRoute)
		r.Delete("/admin/api/routes/{name}", s.deleteRoute)

		r.Get("/admin/api/plans", s.listPlans)
		r.Post("/admin/api/plans", s.createPlan)
		r.Put("/admin/api/plans/{id}", s.putPlan)
		r.Delete("/admin/api/plans/{id}", s.deletePlan)

		r.Get("/admin/api/consumers", s.listConsumers)
		r.Post("/admin/api/consumers", s.createConsumer)
		r.Get("/admin/api/consumers/{id}", s.getConsumer)
		r.Put("/admin/api/consumers/{id}", s.putConsumer)
		r.Delete("/admin/api/consumers/{id}", s.deleteConsumer)

		r.Get("/admin/api/consumers/{id}/api-keys", s.listKeys)
		r.Post("/admin/api/consumers/{id}/api-keys", s.createKey)
		r.Delete("/admin/api/api-keys/{id}", s.revokeKey)
	})
	return r
}

func healthHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Service) loginHandler(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	token, err := s.Login(r.Context(), body.Username, body.Password)
	if err != nil {
		if err == ErrInvalidCredentials {
			writeError(w, http.StatusUnauthorized, "invalid credentials")
			return
		}
		s.logger.Error("admin login failed", "error", err)
		writeError(w, http.StatusInternalServerError, "login failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"token": token})
}

func meHandler(w http.ResponseWriter, r *http.Request) {
	username, _ := AdminUserFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]string{"username": username})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
