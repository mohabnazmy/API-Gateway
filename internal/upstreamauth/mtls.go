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
	bt, ok := base.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("mtls: base transport is not *http.Transport")
	}
	cert, err := loadCert(cfg)
	if err != nil {
		return nil, fmt.Errorf("mtls: %w", err)
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
