package upstreamauth

import (
	"fmt"
	"os"
	"strings"
)

// resolveSecretBytes resolves a secret reference to its raw bytes:
//
//	"env:NAME"   reads environment variable NAME
//	"file:/path" reads the file at /path
//	anything else is used as a literal value
//
// It is used for tokens, client secrets, and TLS cert/key material so secrets
// need not be inlined in route configuration.
func resolveSecretBytes(ref string) ([]byte, error) {
	switch {
	case strings.HasPrefix(ref, "env:"):
		name := ref[len("env:"):]
		v, ok := os.LookupEnv(name)
		if !ok {
			return nil, fmt.Errorf("env %q is not set", name)
		}
		return []byte(v), nil
	case strings.HasPrefix(ref, "file:"):
		b, err := os.ReadFile(ref[len("file:"):])
		if err != nil {
			return nil, err
		}
		return b, nil
	default:
		return []byte(ref), nil
	}
}

// resolveSecret resolves a reference like resolveSecretBytes but returns a
// trimmed string, suitable for tokens and client secrets.
func resolveSecret(ref string) (string, error) {
	b, err := resolveSecretBytes(ref)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}
