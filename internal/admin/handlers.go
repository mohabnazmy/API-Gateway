package admin

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/proxy"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

// --- routes (keyed by name) ---

func (s *Service) listRoutes(w http.ResponseWriter, r *http.Request) {
	routes, err := s.store.ListRoutes(r.Context())
	if err != nil {
		s.serverError(w, "list routes", err)
		return
	}
	writeJSON(w, http.StatusOK, routes)
}

func (s *Service) getRoute(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	rt, ok, err := s.findRoute(r.Context(), name)
	if err != nil {
		s.serverError(w, "get route", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	writeJSON(w, http.StatusOK, rt)
}

func (s *Service) createRoute(w http.ResponseWriter, r *http.Request) {
	var rt model.Route
	if !decode(w, r, &rt) {
		return
	}
	if msg := validateRoute(rt); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if _, exists, err := s.findRoute(r.Context(), rt.Name); err != nil {
		s.serverError(w, "create route", err)
		return
	} else if exists {
		writeError(w, http.StatusConflict, "a route with that name already exists")
		return
	}
	if err := s.store.UpsertRoute(r.Context(), rt); err != nil {
		s.serverError(w, "create route", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusCreated, rt)
}

func (s *Service) putRoute(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var rt model.Route
	if !decode(w, r, &rt) {
		return
	}
	rt.Name = name // the URL is authoritative for identity
	if msg := validateRoute(rt); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if _, exists, err := s.findRoute(r.Context(), name); err != nil {
		s.serverError(w, "update route", err)
		return
	} else if !exists {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	if err := s.store.UpsertRoute(r.Context(), rt); err != nil {
		s.serverError(w, "update route", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusOK, rt)
}

func (s *Service) deleteRoute(w http.ResponseWriter, r *http.Request) {
	ok, err := s.store.DeleteRoute(r.Context(), chi.URLParam(r, "name"))
	if err != nil {
		s.serverError(w, "delete route", err)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "route not found")
		return
	}
	s.reloadAfterWrite(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) findRoute(ctx context.Context, name string) (model.Route, bool, error) {
	routes, err := s.store.ListRoutes(ctx)
	if err != nil {
		return model.Route{}, false, err
	}
	for _, rt := range routes {
		if rt.Name == name {
			return rt, true, nil
		}
	}
	return model.Route{}, false, nil
}

// --- plans ---

func (s *Service) listPlans(w http.ResponseWriter, r *http.Request) {
	plans, err := s.store.ListPlans(r.Context())
	if err != nil {
		s.serverError(w, "list plans", err)
		return
	}
	writeJSON(w, http.StatusOK, plans)
}

func (s *Service) createPlan(w http.ResponseWriter, r *http.Request) {
	var p model.Plan
	if !decode(w, r, &p) {
		return
	}
	p.ID = 0
	if msg := validatePlan(p); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	id, err := s.store.UpsertPlan(r.Context(), p)
	if err != nil {
		s.serverError(w, "create plan", err)
		return
	}
	p.ID = id
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusCreated, p)
}

func (s *Service) putPlan(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetPlan(r.Context(), id); err != nil {
		s.serverError(w, "update plan", err)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	var p model.Plan
	if !decode(w, r, &p) {
		return
	}
	p.ID = id
	if msg := validatePlan(p); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if _, err := s.store.UpsertPlan(r.Context(), p); err != nil {
		s.serverError(w, "update plan", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusOK, p)
}

func (s *Service) deletePlan(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	deleted, err := s.store.DeletePlan(r.Context(), id)
	if err != nil {
		s.serverError(w, "delete plan", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "plan not found")
		return
	}
	s.reloadAfterWrite(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// --- consumers ---

func (s *Service) listConsumers(w http.ResponseWriter, r *http.Request) {
	consumers, err := s.store.ListConsumers(r.Context())
	if err != nil {
		s.serverError(w, "list consumers", err)
		return
	}
	writeJSON(w, http.StatusOK, consumers)
}

func (s *Service) getConsumer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	c, found, err := s.store.GetConsumer(r.Context(), id)
	if err != nil {
		s.serverError(w, "get consumer", err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "consumer not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Service) createConsumer(w http.ResponseWriter, r *http.Request) {
	var c model.Consumer
	if !decode(w, r, &c) {
		return
	}
	c.ID = 0
	if msg := s.validateConsumer(r.Context(), c); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	id, err := s.store.UpsertConsumer(r.Context(), c)
	if err != nil {
		s.serverError(w, "create consumer", err)
		return
	}
	c.ID = id
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusCreated, c)
}

func (s *Service) putConsumer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetConsumer(r.Context(), id); err != nil {
		s.serverError(w, "update consumer", err)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "consumer not found")
		return
	}
	var c model.Consumer
	if !decode(w, r, &c) {
		return
	}
	c.ID = id
	if msg := s.validateConsumer(r.Context(), c); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	if _, err := s.store.UpsertConsumer(r.Context(), c); err != nil {
		s.serverError(w, "update consumer", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	writeJSON(w, http.StatusOK, c)
}

func (s *Service) deleteConsumer(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	deleted, err := s.store.DeleteConsumer(r.Context(), id)
	if err != nil {
		s.serverError(w, "delete consumer", err)
		return
	}
	if !deleted {
		writeError(w, http.StatusNotFound, "consumer not found")
		return
	}
	s.reloadAfterWrite(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// --- api keys ---

func (s *Service) listKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	keys, err := s.store.ListConsumerKeys(r.Context(), id)
	if err != nil {
		s.serverError(w, "list keys", err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Service) createKey(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetConsumer(r.Context(), id); err != nil {
		s.serverError(w, "create key", err)
		return
	} else if !found {
		writeError(w, http.StatusNotFound, "consumer not found")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	if strings.TrimSpace(body.Name) == "" {
		writeError(w, http.StatusUnprocessableEntity, "name is required")
		return
	}
	plaintext, err := generateAPIKey()
	if err != nil {
		s.serverError(w, "generate key", err)
		return
	}
	keyID, err := s.store.CreateAPIKey(r.Context(), id, body.Name, store.HashAPIKey(plaintext))
	if err != nil {
		s.serverError(w, "create key", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	// The plaintext key is returned exactly once and never stored.
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": keyID, "consumer_id": id, "name": body.Name, "key": plaintext,
	})
}

func (s *Service) revokeKey(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	revoked, err := s.store.RevokeAPIKey(r.Context(), id)
	if err != nil {
		s.serverError(w, "revoke key", err)
		return
	}
	if !revoked {
		writeError(w, http.StatusNotFound, "key not found or already revoked")
		return
	}
	s.reloadAfterWrite(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

// --- validation ---

// validateRoute returns a human-readable message when r is invalid, or "" when
// it is valid. It reuses the data-plane compiler so admin validation can never
// drift from what the proxy actually accepts.
func validateRoute(r model.Route) string {
	if strings.TrimSpace(r.Name) == "" {
		return "name is required"
	}
	if !strings.HasPrefix(r.PathPrefix, "/") {
		return "path_prefix must start with '/'"
	}
	snap, err := proxy.NewSnapshot([]model.Route{r}, slogDiscard())
	if err != nil {
		return err.Error()
	}
	snap.Close()
	return ""
}

func validatePlan(p model.Plan) string {
	switch {
	case strings.TrimSpace(p.Name) == "":
		return "name is required"
	case p.RPS <= 0:
		return "rps must be > 0"
	case p.Burst <= 0:
		return "burst must be > 0"
	}
	return ""
}

func (s *Service) validateConsumer(ctx context.Context, c model.Consumer) string {
	if strings.TrimSpace(c.Name) == "" {
		return "name is required"
	}
	if c.PlanID != 0 {
		if _, ok, err := s.store.GetPlan(ctx, c.PlanID); err != nil {
			return "could not verify plan"
		} else if !ok {
			return fmt.Sprintf("plan %d does not exist", c.PlanID)
		}
	}
	return ""
}

// --- helpers ---

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return id, true
}

func (s *Service) serverError(w http.ResponseWriter, op string, err error) {
	s.logger.Error("admin "+op+" failed", "error", err)
	writeError(w, http.StatusInternalServerError, op+" failed")
}

// slogDiscard is used during route validation so compiling a candidate route
// doesn't emit data-plane "route registered" logs.
func slogDiscard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// generateAPIKey returns a random, URL-safe secret with a recognizable prefix.
func generateAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "gwk_" + base64.RawURLEncoding.EncodeToString(b), nil
}
