// Package upstreamauth authenticates the gateway's outbound requests to upstream
// services. Each mode (e.g. Google OIDC) implements Authenticator; New selects
// one by the route's configured type. The proxy depends only on the
// Authenticator interface here — not on any specific provider — so the gateway
// stays vendor-neutral and a deployment compiles in only the modes it uses.
//
// See docs/upstream-auth-design.md for the broader design and planned modes.
package upstreamauth

import (
	"context"
	"fmt"
	"net/http"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// Authenticator mutates an outbound request so the upstream accepts it. It is
// built once per route and invoked on every proxied request, after the path has
// been rewritten. "Attach a token" and "sign the request" are both just ways of
// mutating out, so one interface covers every mode.
type Authenticator interface {
	Apply(ctx context.Context, out *http.Request) error
}

// New builds the Authenticator for a route's upstream-auth config. It returns
// (nil, nil) when the route needs no upstream auth, and an error for an unknown
// type.
//
// defaultAudience is the upstream origin (scheme://host); token-minting modes
// use it when the route does not override the audience.
func New(cfg model.UpstreamAuth, defaultAudience string) (Authenticator, error) {
	switch cfg.Type {
	case "", "none":
		return nil, nil
	case "bearer":
		return newBearer(cfg)
	case "google_oidc":
		audience := cfg.Audience
		if audience == "" {
			audience = defaultAudience
		}
		return newGoogleOIDC(audience), nil
	case "oauth2_client_credentials":
		return newOAuth2(cfg, defaultAudience)
	case "aws_sigv4":
		return newSigV4(cfg)
	case "mtls":
		// mTLS is applied at the transport layer (see Transport), not by mutating
		// the request, so there is no request Authenticator for it.
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown upstream_auth type %q", cfg.Type)
	}
}
