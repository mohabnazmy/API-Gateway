package upstreamauth

import (
	"context"
	"net/http"
	"sync"

	"github.com/mohabnazmy/API-Gateway/internal/gcpauth"
)

// tokenSource mints identity tokens for an audience. *gcpauth.IDTokenSource
// implements it; keeping it an interface lets the Google authenticator be tested
// without the GCP metadata server.
type tokenSource interface {
	Token(ctx context.Context, audience string) (string, error)
}

// The Google token source is created on first use, so a gateway with no
// google_oidc routes never constructs it, and all google_oidc routes share one
// per-audience token cache.
var (
	googleOnce   sync.Once
	googleShared tokenSource
)

func defaultGoogleSource() tokenSource {
	googleOnce.Do(func() { googleShared = gcpauth.NewIDTokenSource() })
	return googleShared
}

// googleOIDC attaches a Google-signed identity token (audience = the upstream
// origin, unless overridden) as a Bearer token, so the gateway can call a
// private Cloud Run service.
type googleOIDC struct {
	audience string
	source   tokenSource
}

func newGoogleOIDC(audience string) *googleOIDC {
	return &googleOIDC{audience: audience, source: defaultGoogleSource()}
}

func (g *googleOIDC) Apply(ctx context.Context, out *http.Request) error {
	token, err := g.source.Token(ctx, g.audience)
	if err != nil {
		return err
	}
	out.Header.Set("Authorization", "Bearer "+token)
	return nil
}
