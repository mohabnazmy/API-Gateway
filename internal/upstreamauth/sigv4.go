package upstreamauth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// sigv4 signs outbound requests with AWS Signature Version 4, so the gateway can
// call private AWS targets (API Gateway, Lambda function URLs, ALB with IAM).
// Unlike the token modes it must read the request body to hash it.
type sigv4 struct {
	signer  *v4.Signer
	creds   aws.CredentialsProvider
	region  string
	service string
	now     func() time.Time // injectable for tests
}

func newSigV4(cfg model.UpstreamAuth) (*sigv4, error) {
	if cfg.Region == "" {
		return nil, fmt.Errorf("aws_sigv4: region is required")
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("aws_sigv4: load credentials: %w", err)
	}
	service := cfg.Service
	if service == "" {
		service = "execute-api" // API Gateway; override for lambda, etc.
	}
	return &sigv4{
		signer:  v4.NewSigner(),
		creds:   awsCfg.Credentials,
		region:  cfg.Region,
		service: service,
		now:     time.Now,
	}, nil
}

func (s *sigv4) Apply(ctx context.Context, out *http.Request) error {
	// SigV4 signs a hash of the body, but the reverse proxy streams it — buffer
	// it, hash it, then restore a fresh reader for forwarding.
	var body []byte
	if out.Body != nil {
		b, err := io.ReadAll(out.Body)
		_ = out.Body.Close()
		if err != nil {
			return fmt.Errorf("aws_sigv4: read body: %w", err)
		}
		body = b
		out.Body = io.NopCloser(bytes.NewReader(body))
		out.ContentLength = int64(len(body))
		out.GetBody = func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(body)), nil
		}
	}
	sum := sha256.Sum256(body)
	payloadHash := hex.EncodeToString(sum[:])

	creds, err := s.creds.Retrieve(ctx)
	if err != nil {
		return fmt.Errorf("aws_sigv4: retrieve credentials: %w", err)
	}
	return s.signer.SignHTTP(ctx, creds, out, payloadHash, s.service, s.region, s.now())
}
