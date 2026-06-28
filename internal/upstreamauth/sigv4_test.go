package upstreamauth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func TestSigV4(t *testing.T) {
	// Static credentials via the env provider keep LoadDefaultConfig offline.
	t.Setenv("AWS_ACCESS_KEY_ID", "AKIDEXAMPLE")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secretkey")
	t.Setenv("AWS_SESSION_TOKEN", "")

	a, err := newSigV4(model.UpstreamAuth{Region: "us-east-1", Service: "execute-api"})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := http.NewRequest(http.MethodPost, "https://api.example.com/v1/things", strings.NewReader("payload-body"))
	if err := a.Apply(context.Background(), req); err != nil {
		t.Fatal(err)
	}

	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("Authorization = %q, want SigV4", auth)
	}
	if !strings.Contains(auth, "/us-east-1/execute-api/aws4_request") {
		t.Fatalf("credential scope missing region/service: %q", auth)
	}
	if req.Header.Get("X-Amz-Date") == "" {
		t.Fatal("X-Amz-Date not set")
	}
	// Body must survive signing so it can still be forwarded.
	body, _ := io.ReadAll(req.Body)
	if string(body) != "payload-body" {
		t.Fatalf("body after signing = %q", body)
	}
}

func TestSigV4RequiresRegion(t *testing.T) {
	if _, err := newSigV4(model.UpstreamAuth{}); err == nil {
		t.Fatal("expected error for missing region")
	}
}
