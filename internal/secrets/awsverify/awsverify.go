// Package awsverify checks whether AWS access keys found by a secret scan are
// live, with signed STS GetCallerIdentity requests (at most three per key). SigV4 signs the
// request with the secret, so the secret itself is never transmitted.
package awsverify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/x-mesh/gk/internal/secrets"
)

// Status is the outcome of one key check.
type Status string

const (
	// StatusValid means STS accepted the key at the time of the call.
	StatusValid Status = "valid"
	// StatusRejected means STS rejected the key ID or its token as invalid
	// or expired. A key from another partition (China, GovCloud) is also
	// rejected by the global endpoint, so this does not prove the key is dead.
	StatusRejected Status = "rejected"
	// StatusUnknown means the check ran but did not decide: a network error,
	// a timeout, or a signature that no nearby secret produced.
	StatusUnknown Status = "unknown"
	// StatusUnverifiable means no check ran: the key type is not supported,
	// or the secret or session token was not found next to the key ID.
	StatusUnverifiable Status = "unverifiable"
)

// Result is the check outcome for one key ID. Code is the STS error code or
// a local reason when Status is not valid.
type Result struct {
	Status  Status
	Account string
	ARN     string
	Code    string
}

// Local reason codes for StatusUnverifiable and StatusUnknown.
const (
	CodeUnsupportedKeyType   = "unsupported_key_type"
	CodeSecretNotFound       = "secret_not_found"
	CodeSessionTokenNotFound = "session_token_not_found"
	CodeRequestFailed        = "request_failed"
)

const (
	defaultEndpoint = "https://sts.amazonaws.com/"
	// The global STS endpoint signs in us-east-1.
	signingRegion  = "us-east-1"
	signingService = "sts"
	requestBody    = "Action=GetCallerIdentity&Version=2011-06-15"
	callTimeout    = 15 * time.Second
	defaultWorkers = 4
	// maxAttemptsPerKey bounds the STS calls for one key ID after the
	// candidates from every finding of that key are merged.
	maxAttemptsPerKey = 3
)

// Verifier sends the STS requests. The zero value uses the global endpoint.
type Verifier struct {
	Endpoint string
	Client   *http.Client
	Now      func() time.Time
	Workers  int
}

// Verify checks every distinct key ID once. Credentials that share a key ID
// (the same key in several files or commits) are merged, so a history scan
// does not repeat calls. The result maps the raw key ID to its outcome.
func (v *Verifier) Verify(ctx context.Context, creds []*secrets.AWSCredential) map[string]Result {
	merged := mergeByKeyID(creds)
	results := make(map[string]Result, len(merged))
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(v.workers())
	for _, c := range merged {
		g.Go(func() error {
			r := v.check(ctx, c)
			mu.Lock()
			results[c.keyID] = r
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return results
}

type mergedCred struct {
	keyID   string
	secrets []string
	token   string
}

func mergeByKeyID(creds []*secrets.AWSCredential) []*mergedCred {
	byID := map[string]*mergedCred{}
	var order []*mergedCred
	for _, c := range creds {
		if c == nil {
			continue
		}
		m, ok := byID[c.KeyID()]
		if !ok {
			m = &mergedCred{keyID: c.KeyID()}
			byID[c.KeyID()] = m
			order = append(order, m)
		}
		for _, s := range c.SecretCandidates() {
			if !slices.Contains(m.secrets, s) {
				m.secrets = append(m.secrets, s)
			}
		}
		if m.token == "" {
			m.token = c.SessionToken()
		}
	}
	return order
}

// check tries the secret candidates in order. A wrong secret fails with
// SignatureDoesNotMatch, so the next candidate is tried; any other answer
// decides the key.
func (v *Verifier) check(ctx context.Context, c *mergedCred) Result {
	switch {
	case !strings.HasPrefix(c.keyID, "AKIA") && !strings.HasPrefix(c.keyID, "ASIA"):
		return Result{Status: StatusUnverifiable, Code: CodeUnsupportedKeyType}
	case len(c.secrets) == 0:
		return Result{Status: StatusUnverifiable, Code: CodeSecretNotFound}
	case strings.HasPrefix(c.keyID, "ASIA") && c.token == "":
		return Result{Status: StatusUnverifiable, Code: CodeSessionTokenNotFound}
	}
	var last Result
	for _, secret := range c.secrets[:min(len(c.secrets), maxAttemptsPerKey)] {
		last = v.call(ctx, c.keyID, secret, c.token)
		if last.Code != "SignatureDoesNotMatch" {
			return last
		}
	}
	return last
}

type callerIdentityResponse struct {
	Account string `xml:"GetCallerIdentityResult>Account"`
	ARN     string `xml:"GetCallerIdentityResult>Arn"`
}

type errorResponse struct {
	Code string `xml:"Error>Code"`
}

func (v *Verifier) call(ctx context.Context, keyID, secret, token string) Result {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := v.newRequest(ctx, keyID, secret, token)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	resp, err := v.client().Do(req)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if resp.StatusCode == http.StatusOK {
		var ok callerIdentityResponse
		if err := xml.Unmarshal(body, &ok); err != nil || ok.Account == "" {
			return Result{Status: StatusUnknown, Code: CodeRequestFailed}
		}
		return Result{Status: StatusValid, Account: ok.Account, ARN: ok.ARN}
	}
	var e errorResponse
	if err := xml.Unmarshal(body, &e); err != nil || e.Code == "" {
		return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", resp.StatusCode)}
	}
	switch e.Code {
	case "InvalidClientTokenId", "ExpiredToken":
		return Result{Status: StatusRejected, Code: e.Code}
	default:
		return Result{Status: StatusUnknown, Code: e.Code}
	}
}

func (v *Verifier) newRequest(ctx context.Context, keyID, secret, token string) (*http.Request, error) {
	endpoint := v.Endpoint
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	if req.URL.Host == "" {
		return nil, errors.New("awsverify: endpoint has no host")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	sign(req, keyID, secret, token, v.now())
	return req, nil
}

// sign adds SigV4 headers for requestBody. The canonical request covers the
// content length and type, host, date, and session token headers.
func sign(req *http.Request, keyID, secret, token string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("X-Amz-Date", amzDate)
	if token != "" {
		req.Header.Set("X-Amz-Security-Token", token)
	}
	headers := [][2]string{
		{"content-length", strconv.Itoa(len(requestBody))},
		{"content-type", req.Header.Get("Content-Type")},
		{"host", req.URL.Host},
		{"x-amz-date", amzDate},
	}
	if token != "" {
		headers = append(headers, [2]string{"x-amz-security-token", token})
	}
	var canonHeaders strings.Builder
	names := make([]string, 0, len(headers))
	for _, h := range headers {
		canonHeaders.WriteString(h[0] + ":" + strings.TrimSpace(h[1]) + "\n")
		names = append(names, h[0])
	}
	signed := strings.Join(names, ";")
	path := req.URL.EscapedPath()
	if path == "" {
		path = "/"
	}
	canonical := strings.Join([]string{
		req.Method, path, req.URL.RawQuery, canonHeaders.String(), signed, hexSHA256(requestBody),
	}, "\n")
	scope := day + "/" + signingRegion + "/" + signingService + "/aws4_request"
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hexSHA256(canonical)
	key := hmacSHA256([]byte("AWS4"+secret), day)
	key = hmacSHA256(key, signingRegion)
	key = hmacSHA256(key, signingService)
	key = hmacSHA256(key, "aws4_request")
	sig := hex.EncodeToString(hmacSHA256(key, toSign))
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+keyID+"/"+scope+", SignedHeaders="+signed+", Signature="+sig)
}

func hexSHA256(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func (v *Verifier) client() *http.Client {
	if v.Client != nil {
		return v.Client
	}
	return http.DefaultClient
}

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

func (v *Verifier) workers() int {
	if v.Workers > 0 {
		return v.Workers
	}
	return defaultWorkers
}
