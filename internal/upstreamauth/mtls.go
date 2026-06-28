package upstreamauth

import (
	"crypto/tls"
	"fmt"
	"net/http"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// Transport returns the RoundTripper a route should use for forwarding. For most
// modes it returns base unchanged; for "mtls" it returns a clone of base that
// presents a client certificate (mutual TLS is a transport-layer concern, so it
// cannot be expressed as an Authenticator that mutates the request).
//
// base must be the *http.Transport the proxy would otherwise share across routes.
func Transport(cfg model.UpstreamAuth, base http.RoundTripper) (http.RoundTripper, error) {
	if cfg.Type != "mtls" {
		return base, nil
	}
	cert, err := loadCert(cfg)
	if err != nil {
		return nil, fmt.Errorf("mtls: %w", err)
	}

	// Clone the base transport to preserve its timeouts/proxy settings. If base
	// is a wrapping RoundTripper we can't reach into, fall back to a default
	// transport so the route still works instead of failing the whole snapshot.
	bt, ok := base.(*http.Transport)
	if !ok {
		bt = http.DefaultTransport.(*http.Transport)
	}
	clone := bt.Clone()
	if clone.TLSClientConfig == nil {
		clone.TLSClientConfig = &tls.Config{}
	} else {
		clone.TLSClientConfig = clone.TLSClientConfig.Clone()
	}
	clone.TLSClientConfig.Certificates = []tls.Certificate{cert}
	return clone, nil
}

func loadCert(cfg model.UpstreamAuth) (tls.Certificate, error) {
	if cfg.CertRef == "" || cfg.KeyRef == "" {
		return tls.Certificate{}, fmt.Errorf("cert_ref and key_ref are required")
	}
	certPEM, err := resolveSecretBytes(cfg.CertRef)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cert_ref: %w", err)
	}
	keyPEM, err := resolveSecretBytes(cfg.KeyRef)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("key_ref: %w", err)
	}
	return tls.X509KeyPair(certPEM, keyPEM)
}
