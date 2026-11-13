// Package verify checks whether credentials found by a secret scan are live,
// with one read-only identity call to the issuer:
//
//   - AWS: STS GetCallerIdentity, signed with SigV4. The secret signs the
//     request and is not sent.
//   - Slack: auth.test, with the token in the Authorization header.
//   - GCP API key: the Discovery API, with the key in the X-Goog-Api-Key
//     header.
//   - OCI API signing key: Identity ListApiKeys for the key's own user,
//     signed with the private key (HTTP Signature, rsa-sha256). The key signs
//     the request and is not sent.
//
// Slack tokens and GCP API keys are bearer values, so those calls send the
// value itself to slack.com or googleapis.com.
package verify

import (
	"context"
	"crypto/rsa"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/x-mesh/gk/internal/secrets"
)

// Status is the outcome of one credential check.
type Status string

const (
	// StatusValid means the issuer accepted the credential at the time of
	// the call. For a GCP API key it also covers a key that exists but whose
	// restrictions block the call; Code then names the restriction.
	StatusValid Status = "valid"
	// StatusRejected means the issuer said the credential itself is invalid,
	// revoked, or expired. The global STS endpoint also rejects an AWS key
	// from another partition (China, GovCloud), and OCI gives the same 401
	// for a region the tenancy is not subscribed to.
	StatusRejected Status = "rejected"
	// StatusUnknown means the check ran but did not decide: a network error,
	// a rate limit, a timeout, or an answer that does not judge the
	// credential.
	StatusUnknown Status = "unknown"
	// StatusUnverifiable means no check ran: the key type is not supported,
	// or the AWS secret or session token was not found next to the key ID.
	StatusUnverifiable Status = "unverifiable"
)

// Result is the check outcome for one credential. Account is the AWS
// account, Slack team ID, GCP consumer project, or OCI tenancy OCID; ARN is
// the AWS caller; Identity is the Slack user ID or OCI user OCID. AccountName, URL, and IdentityName are the
// Slack workspace name, workspace URL, and user name. Code is the issuer's
// error code or a local reason, and on a valid GCP key the restriction that
// blocked the call.
type Result struct {
	Status       Status
	Account      string
	AccountName  string
	URL          string
	ARN          string
	Identity     string
	IdentityName string
	Code         string
}

// Local reason codes.
const (
	CodeUnsupportedKeyType   = "unsupported_key_type"
	CodeSecretNotFound       = "secret_not_found"
	CodeSessionTokenNotFound = "session_token_not_found"
	CodeRequestFailed        = "request_failed"
)

const (
	callTimeout    = 15 * time.Second
	defaultWorkers = 4
	maxBodyBytes   = 1 << 20
)

// Verifier sends the checks. Empty endpoints use the issuers' public ones.
type Verifier struct {
	AWSEndpoint   string
	SlackEndpoint string
	GCPEndpoint   string
	// OCIEndpoint replaces https://identity.{region}.oci.oraclecloud.com.
	OCIEndpoint string
	Client      *http.Client
	Now         func() time.Time
	Workers     int
}

// Verify checks every distinct credential once and maps Credential.ID to its
// result. The same value found in several files or commits is merged, so a
// history scan does not repeat calls.
func (v *Verifier) Verify(ctx context.Context, creds []*secrets.Credential) map[string]Result {
	merged := merge(creds)
	results := make(map[string]Result, len(merged))
	var mu sync.Mutex
	var g errgroup.Group
	g.SetLimit(v.workers())
	for _, c := range merged {
		g.Go(func() error {
			var r Result
			switch c.provider {
			case secrets.ProviderAWS:
				r = v.checkAWS(ctx, c)
			case secrets.ProviderSlack:
				r = v.checkSlack(ctx, c)
			case secrets.ProviderGCP:
				r = v.checkGCP(ctx, c)
			case secrets.ProviderOCI:
				r = v.checkOCI(ctx, c)
			default:
				r = Result{Status: StatusUnverifiable, Code: CodeUnsupportedKeyType}
			}
			mu.Lock()
			results[c.id] = r
			mu.Unlock()
			return nil
		})
	}
	_ = g.Wait()
	return results
}

type mergedCred struct {
	id       string
	provider string
	kind     string
	value    string
	secrets  []string
	token    string
	// OCI: the parsed signing key and the profiles that name it.
	ociKey      *rsa.PrivateKey
	ociProfiles []secrets.OCIProfile
}

func merge(creds []*secrets.Credential) []*mergedCred {
	byID := map[string]*mergedCred{}
	var order []*mergedCred
	for _, c := range creds {
		if c == nil {
			continue
		}
		m, ok := byID[c.ID()]
		if !ok {
			m = &mergedCred{id: c.ID(), provider: c.Provider(), kind: c.Kind(), value: c.Value()}
			byID[c.ID()] = m
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
		if m.ociKey == nil {
			m.ociKey = c.OCIKey()
		}
		for _, p := range c.OCIProfiles() {
			if len(m.ociProfiles) < maxOCIAttempts && !slices.Contains(m.ociProfiles, p) {
				m.ociProfiles = append(m.ociProfiles, p)
			}
		}
	}
	return order
}

// do sends req and reads a bounded body. Redirects are not followed, so a
// credential header never reaches a host other than the issuer's endpoint.
func (v *Verifier) do(req *http.Request) (int, []byte, error) {
	status, _, body, err := v.send(req)
	return status, body, err
}

// send is do that also returns the response headers.
func (v *Verifier) send(req *http.Request) (int, http.Header, []byte, error) {
	client := *v.client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return 0, nil, nil, err
	}
	return resp.StatusCode, resp.Header, body, nil
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
