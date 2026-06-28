package upstreamauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// oauth2CC implements the OAuth2 client-credentials grant: it fetches an access
// token from a token endpoint and caches it until shortly before expiry, then
// attaches it as a Bearer token. Works with any compliant issuer (Auth0, Okta,
// Keycloak, Azure AD, ...).
type oauth2CC struct {
	tokenURL string
	clientID string
	secret   string
	scope    string // space-joined
	audience string // optional (e.g. Auth0)
	client   *http.Client

	mu     sync.Mutex
	token  string
	expiry time.Time
	now    func() time.Time // injectable for tests
}

func newOAuth2(cfg model.UpstreamAuth, defaultAudience string) (*oauth2CC, error) {
	if cfg.TokenURL == "" {
		return nil, fmt.Errorf("oauth2_client_credentials: token_url is required")
	}
	if cfg.ClientID == "" {
		return nil, fmt.Errorf("oauth2_client_credentials: client_id is required")
	}
	secret, err := resolveSecret(cfg.ClientSecretRef)
	if err != nil {
		return nil, fmt.Errorf("oauth2_client_credentials: %w", err)
	}
	audience := cfg.Audience
	if audience == "" {
		audience = defaultAudience
	}
	return &oauth2CC{
		tokenURL: cfg.TokenURL,
		clientID: cfg.ClientID,
		secret:   secret,
		scope:    strings.Join(cfg.Scopes, " "),
		audience: audience,
		client:   &http.Client{Timeout: 10 * time.Second},
		now:      time.Now,
	}, nil
}

func (o *oauth2CC) Apply(ctx context.Context, out *http.Request) error {
	token, err := o.accessToken(ctx)
	if err != nil {
		return err
	}
	out.Header.Set("Authorization", "Bearer "+token)
	return nil
}

func (o *oauth2CC) accessToken(ctx context.Context) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.token != "" && o.now().Before(o.expiry) {
		return o.token, nil
	}
	token, ttl, err := o.fetch(ctx)
	if err != nil {
		return "", err
	}
	o.token = token
	// Refresh a minute early to avoid racing expiry; clamp tiny TTLs.
	if ttl > time.Minute {
		ttl -= time.Minute
	}
	o.expiry = o.now().Add(ttl)
	return token, nil
}

func (o *oauth2CC) fetch(ctx context.Context) (string, time.Duration, error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if o.scope != "" {
		form.Set("scope", o.scope)
	}
	if o.audience != "" {
		form.Set("audience", o.audience)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(o.clientID, o.secret) // RFC 6749 §2.3.1

	resp, err := o.client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var tr struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", 0, fmt.Errorf("decode token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("token endpoint returned no access_token")
	}
	ttl := time.Duration(tr.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour // issuer omitted expires_in; assume a conservative default
	}
	return tr.AccessToken, ttl, nil
}
