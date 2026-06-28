package upstreamauth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// writeSelfSignedPair generates a throwaway cert/key and returns their file paths.
func writeSelfSignedPair(t *testing.T) (certPath, keyPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "gateway-client"},
		NotBefore:    time.Unix(0, 0),
		NotAfter:     time.Unix(1<<31-1, 0),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "client.crt")
	keyPath = filepath.Join(dir, "client.key")
	writePEM(t, certPath, "CERTIFICATE", der)
	writePEM(t, keyPath, "EC PRIVATE KEY", keyDER)
	return certPath, keyPath
}

func writePEM(t *testing.T, path, typ string, der []byte) {
	t.Helper()
	b := pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTransportPassthrough(t *testing.T) {
	base := &http.Transport{}
	got, err := Transport(model.UpstreamAuth{Type: "bearer"}, base)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatal("non-mtls should return the base transport unchanged")
	}
}

func TestTransportMTLS(t *testing.T) {
	certPath, keyPath := writeSelfSignedPair(t)
	base := &http.Transport{}
	got, err := Transport(model.UpstreamAuth{
		Type:    "mtls",
		CertRef: "file:" + certPath,
		KeyRef:  "file:" + keyPath,
	}, base)
	if err != nil {
		t.Fatal(err)
	}
	rt, ok := got.(*http.Transport)
	if !ok {
		t.Fatalf("got %T, want *http.Transport", got)
	}
	if rt == base {
		t.Fatal("mtls should return a clone, not mutate the base transport")
	}
	if rt.TLSClientConfig == nil || len(rt.TLSClientConfig.Certificates) != 1 {
		t.Fatalf("client certificate not configured")
	}
	// Transport.Clone() lazily gives base an empty TLSClientConfig, but the client
	// certificate must land only on the per-route clone, never on the shared base.
	if base.TLSClientConfig != nil && len(base.TLSClientConfig.Certificates) != 0 {
		t.Fatal("base transport received the client certificate")
	}
}

func TestTransportMTLSMissingRefs(t *testing.T) {
	if _, err := Transport(model.UpstreamAuth{Type: "mtls"}, &http.Transport{}); err == nil {
		t.Fatal("expected error for missing cert_ref/key_ref")
	}
}
