// Package model holds the configuration types shared by the store, registry,
// data plane, and admin API, so all of them agree on one schema.
package model

import (
	"bytes"
	"encoding/json"
)

// Route maps an incoming path prefix to an upstream, with optional auth and
// rate-limit policies. JSON tags describe the Phase 1 bootstrap format; in later
// phases the same struct is populated from the config store.
type Route struct {
	Name        string          `json:"name"`
	PathPrefix  string          `json:"path_prefix"`
	Upstream    string          `json:"upstream"`
	StripPrefix bool            `json:"strip_prefix"`
	Methods     []string        `json:"methods,omitempty"`
	Auth        AuthPolicy      `json:"auth"`
	RateLimit   RateLimitPolicy `json:"rate_limit"`

	// UpstreamAuth selects how the gateway authenticates to the upstream when
	// forwarding. The zero value means no upstream auth.
	UpstreamAuth UpstreamAuth `json:"upstream_auth,omitempty"`
}

// UpstreamAuth selects how (and whether) the gateway authenticates its outbound
// requests to a route's upstream. The zero value (Type "" / "none") means no
// upstream auth. Fields are grouped by the Type that uses them; see
// docs/upstream-auth-design.md.
//
// Secret-bearing fields end in "_ref" and accept "env:NAME" (read an environment
// variable), "file:/path" (read a file), or a literal value — so secrets stay
// out of the route JSON.
type UpstreamAuth struct {
	// Type selects the authentication mode: "" / "none", "bearer", "google_oidc",
	// "oauth2_client_credentials", "aws_sigv4", or "mtls".
	Type string `json:"type,omitempty"`

	// Audience overrides the token audience for token-minting modes
	// (google_oidc, oauth2_client_credentials). Empty = the upstream origin.
	Audience string `json:"audience,omitempty"`

	// bearer: attach a static credential.
	TokenRef string `json:"token_ref,omitempty"` // the token/key (secret ref)
	Header   string `json:"header,omitempty"`    // target header (default "Authorization")
	Scheme   string `json:"scheme,omitempty"`    // value prefix (default "Bearer"; "none" = raw)

	// oauth2_client_credentials: fetch+cache a token via the client-credentials grant.
	TokenURL        string   `json:"token_url,omitempty"`
	ClientID        string   `json:"client_id,omitempty"`
	ClientSecretRef string   `json:"client_secret_ref,omitempty"`
	Scopes          []string `json:"scopes,omitempty"`

	// aws_sigv4: sign requests with SigV4 (credentials from the standard AWS chain).
	Region  string `json:"region,omitempty"`
	Service string `json:"service,omitempty"` // default "execute-api"

	// mtls: present a client certificate at the transport layer.
	CertRef string `json:"cert_ref,omitempty"`
	KeyRef  string `json:"key_ref,omitempty"`
}

// Enabled reports whether the route authenticates to its upstream.
func (u UpstreamAuth) Enabled() bool { return u.Type != "" && u.Type != "none" }

// UnmarshalJSON accepts both the object form ({"type":"google_oidc"}) and the
// legacy bare-string form ("google_oidc"), so configs written against the
// original upstream_auth string keep working.
func (u *UpstreamAuth) UnmarshalJSON(data []byte) error {
	if t := bytes.TrimSpace(data); len(t) > 0 && t[0] == '"' {
		var s string
		if err := json.Unmarshal(t, &s); err != nil {
			return err
		}
		*u = UpstreamAuth{Type: s}
		return nil
	}
	// Object form. The alias avoids recursing back into this method.
	type alias UpstreamAuth
	var a alias
	if err := json.Unmarshal(data, &a); err != nil {
		return err
	}
	*u = UpstreamAuth(a)
	return nil
}

// AuthPolicy describes how (and whether) a route is authenticated.
type AuthPolicy struct {
	// RequireAuth gates the route. When false, the route is public.
	RequireAuth bool `json:"require_auth"`
	// Methods lists accepted credential types: "jwt" and/or "api_key".
	// Empty (with RequireAuth) means accept any configured credential type.
	Methods []string `json:"methods,omitempty"`
}

// AcceptsJWT reports whether the policy accepts a JWT credential.
func (a AuthPolicy) AcceptsJWT() bool { return a.accepts("jwt") }

// AcceptsAPIKey reports whether the policy accepts an API-key credential.
func (a AuthPolicy) AcceptsAPIKey() bool { return a.accepts("api_key") }

func (a AuthPolicy) accepts(method string) bool {
	if len(a.Methods) == 0 {
		return true // unset = accept any configured credential type
	}
	for _, m := range a.Methods {
		if m == method {
			return true
		}
	}
	return false
}

// RateLimitPolicy selects a rate-limit algorithm and its parameters. A zero or
// negative RPS disables limiting for the route.
type RateLimitPolicy struct {
	Algorithm string  `json:"algorithm,omitempty"` // e.g. "token_bucket"
	RPS       float64 `json:"rps"`
	Burst     int     `json:"burst"`
	WindowSec int     `json:"window_sec,omitempty"`
}

// Enabled reports whether the policy should impose a limit.
func (p RateLimitPolicy) Enabled() bool { return p.RPS > 0 }
