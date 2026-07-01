package admin

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/mohabnazmy/API-Gateway/internal/model"
	"github.com/mohabnazmy/API-Gateway/internal/store"
)

// --- plans ---

func (s *Service) plansPage(w http.ResponseWriter, r *http.Request) {
	plans, err := s.store.ListPlans(r.Context())
	if err != nil {
		s.serverError(w, "list plans", err)
		return
	}
	s.render(w, http.StatusOK, "plans", s.page(r, pageData{Plans: plans}))
}

func (s *Service) createPlanUI(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	p, msg := planFromForm(r, 0)
	if msg == "" {
		msg = validatePlan(p)
	}
	if msg != "" {
		s.renderPlansFrag(w, r, msg)
		return
	}
	if _, err := s.store.UpsertPlan(r.Context(), p); err != nil {
		s.serverError(w, "create plan", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderPlansFrag(w, r, "")
}

func (s *Service) editPlanPage(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	p, found, err := s.store.GetPlan(r.Context(), id)
	if err != nil {
		s.serverError(w, "edit plan", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	s.render(w, http.StatusOK, "plan_edit", s.page(r, pageData{Plan: p}))
}

func (s *Service) updatePlanUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetPlan(r.Context(), id); err != nil {
		s.serverError(w, "update plan", err)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	p, msg := planFromForm(r, id)
	if msg == "" {
		msg = validatePlan(p)
	}
	if msg != "" {
		s.render(w, http.StatusUnprocessableEntity, "plan_edit", s.page(r, pageData{Plan: p, Error: msg}))
		return
	}
	if _, err := s.store.UpsertPlan(r.Context(), p); err != nil {
		s.serverError(w, "update plan", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	w.Header().Set("HX-Redirect", "/admin/plans")
	w.WriteHeader(http.StatusOK)
}

func (s *Service) deletePlanUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, err := s.store.DeletePlan(r.Context(), id); err != nil {
		s.serverError(w, "delete plan", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderPlansFrag(w, r, "")
}

func (s *Service) renderPlansFrag(w http.ResponseWriter, r *http.Request, errMsg string) {
	plans, err := s.store.ListPlans(r.Context())
	if err != nil {
		s.serverError(w, "list plans", err)
		return
	}
	s.render(w, http.StatusOK, "plans_frag", s.page(r, pageData{Plans: plans, Error: errMsg}))
}

// --- consumers ---

func (s *Service) consumersPage(w http.ResponseWriter, r *http.Request) {
	s.renderConsumers(w, r, http.StatusOK, "consumers", "")
}

func (s *Service) createConsumerUI(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	c, msg := consumerFromForm(r, 0)
	if msg == "" {
		msg = s.validateConsumer(r.Context(), c)
	}
	if msg != "" {
		s.renderConsumersFrag(w, r, msg)
		return
	}
	if _, err := s.store.UpsertConsumer(r.Context(), c); err != nil {
		s.serverError(w, "create consumer", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderConsumersFrag(w, r, "")
}

func (s *Service) editConsumerPage(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	c, found, err := s.store.GetConsumer(r.Context(), id)
	if err != nil {
		s.serverError(w, "edit consumer", err)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	plans, _ := s.store.ListPlans(r.Context())
	keys, _ := s.store.ListConsumerKeys(r.Context(), id)
	s.render(w, http.StatusOK, "consumer_edit", s.page(r, pageData{Consumer: c, Plans: plans, Keys: keys}))
}

func (s *Service) updateConsumerUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetConsumer(r.Context(), id); err != nil {
		s.serverError(w, "update consumer", err)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	c, msg := consumerFromForm(r, id)
	if msg == "" {
		msg = s.validateConsumer(r.Context(), c)
	}
	if msg != "" {
		plans, _ := s.store.ListPlans(r.Context())
		keys, _ := s.store.ListConsumerKeys(r.Context(), id)
		s.render(w, http.StatusUnprocessableEntity, "consumer_edit", s.page(r, pageData{Consumer: c, Plans: plans, Keys: keys, Error: msg}))
		return
	}
	if _, err := s.store.UpsertConsumer(r.Context(), c); err != nil {
		s.serverError(w, "update consumer", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	w.Header().Set("HX-Redirect", "/admin/consumers")
	w.WriteHeader(http.StatusOK)
}

func (s *Service) deleteConsumerUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, err := s.store.DeleteConsumer(r.Context(), id); err != nil {
		s.serverError(w, "delete consumer", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderConsumersFrag(w, r, "")
}

func (s *Service) renderConsumersFrag(w http.ResponseWriter, r *http.Request, errMsg string) {
	s.renderConsumers(w, r, http.StatusOK, "consumers_frag", errMsg)
}

// renderConsumers renders either the full page or the list fragment with the
// consumer + plan data both need (plans feed the dropdown and the tier column).
func (s *Service) renderConsumers(w http.ResponseWriter, r *http.Request, status int, tmpl, errMsg string) {
	consumers, err := s.store.ListConsumers(r.Context())
	if err != nil {
		s.serverError(w, "list consumers", err)
		return
	}
	plans, _ := s.store.ListPlans(r.Context())
	names := make(map[int64]string, len(plans))
	for _, p := range plans {
		names[p.ID] = p.Name
	}
	s.render(w, status, tmpl, s.page(r, pageData{Consumers: consumers, Plans: plans, PlanNames: names, Error: errMsg}))
}

// --- api keys (issue once, revoke) ---

func (s *Service) createKeyUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, found, err := s.store.GetConsumer(r.Context(), id); err != nil {
		s.serverError(w, "issue key", err)
		return
	} else if !found {
		http.NotFound(w, r)
		return
	}
	_ = r.ParseForm()
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		s.renderKeysFrag(w, r, id, "")
		return
	}
	plaintext, err := generateAPIKey()
	if err != nil {
		s.serverError(w, "issue key", err)
		return
	}
	if _, err := s.store.CreateAPIKey(r.Context(), id, name, store.HashAPIKey(plaintext)); err != nil {
		s.serverError(w, "issue key", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	s.renderKeysFrag(w, r, id, plaintext)
}

func (s *Service) revokeKeyUI(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if _, err := s.store.RevokeAPIKey(r.Context(), id); err != nil {
		s.serverError(w, "revoke key", err)
		return
	}
	s.reloadAfterWrite(r.Context())
	consumerID, _ := strconv.ParseInt(r.URL.Query().Get("consumer"), 10, 64)
	s.renderKeysFrag(w, r, consumerID, "")
}

func (s *Service) renderKeysFrag(w http.ResponseWriter, r *http.Request, consumerID int64, newKey string) {
	keys, err := s.store.ListConsumerKeys(r.Context(), consumerID)
	if err != nil {
		s.serverError(w, "list keys", err)
		return
	}
	s.render(w, http.StatusOK, "keys_frag", s.page(r, pageData{Keys: keys, NewKey: newKey}))
}

// --- form parsing ---

func planFromForm(r *http.Request, id int64) (model.Plan, string) {
	rps, err := strconv.ParseFloat(strings.TrimSpace(r.FormValue("rps")), 64)
	if err != nil {
		return model.Plan{}, "rps must be a number"
	}
	burst, err := strconv.Atoi(strings.TrimSpace(r.FormValue("burst")))
	if err != nil {
		return model.Plan{}, "burst must be a number"
	}
	quota := 0
	if q := strings.TrimSpace(r.FormValue("daily_quota")); q != "" {
		if quota, err = strconv.Atoi(q); err != nil {
			return model.Plan{}, "daily quota must be a number"
		}
	}
	return model.Plan{ID: id, Name: strings.TrimSpace(r.FormValue("name")), RPS: rps, Burst: burst, DailyQuota: quota}, ""
}

func consumerFromForm(r *http.Request, id int64) (model.Consumer, string) {
	var planID int64
	if p := strings.TrimSpace(r.FormValue("plan_id")); p != "" {
		v, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return model.Consumer{}, "plan must be a number"
		}
		planID = v
	}
	return model.Consumer{
		ID:      id,
		Name:    strings.TrimSpace(r.FormValue("name")),
		PlanID:  planID,
		Enabled: r.FormValue("enabled") == "true",
	}, ""
}
