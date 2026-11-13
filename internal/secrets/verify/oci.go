package verify

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/x-mesh/gk/internal/secrets"
)

const (
	ociEndpointFormat = "https://identity.%s.oci.oraclecloud.com"
	ociAPIVersion     = "20160918"
	// maxOCIAttempts bounds the requests for one key when several profiles
	// name its fingerprint.
	maxOCIAttempts = secrets.OCIMaxSendable
	// OCI answers 401 when the client clock is more than five minutes off.
	ociMaxClockSkew     = 5 * time.Minute
	ociNotAuthenticated = "NotAuthenticated"
)

// Local reason codes for OCI.
const (
	CodeRegionNotFound       = secrets.OCIProfileNoRegion
	CodeInvalidProfile       = secrets.OCIProfileInvalid
	CodeUnsupportedRealm     = secrets.OCIProfileUnsupportedRealm
	CodeClockSkew            = "clock_skew"
	CodeFingerprintNotListed = "fingerprint_not_listed"
)

// checkOCI lists the API keys of the profile's own user, a call every OCI
// user may make for itself, signed with the key. When several profiles name
// the fingerprint, every one that can be sent is tried until one is valid. A
// candidate can fail for a reason of its own, such as a region value that
// is not an OCI region, so no failure of one candidate ends the search.
//
// A 401 is rejected only when it is OCI's own answer for the one profile
// that names the key. With several candidates the result is unknown, with
// the code of the nearest one: the 401 can come from a wrong candidate and
// not from the key. OCI also answers 401 for a region the tenancy is not
// subscribed to, so rejected does not prove that the key was deleted.
func (v *Verifier) checkOCI(ctx context.Context, c *mergedCred) Result {
	if c.ociKey == nil || len(c.ociProfiles) == 0 {
		return Result{Status: StatusUnverifiable, Code: CodeUnsupportedKeyType}
	}
	var first, skipped Result
	sent := 0
	for _, p := range c.ociProfiles {
		// The profile values come from repository text and become the
		// request host, path, and key ID. They are checked here, right
		// before the request is built, whatever filtered the list before.
		if code := p.Problem(); code != "" {
			if skipped.Code == "" {
				skipped = Result{Status: StatusUnverifiable, Code: code}
			}
			continue
		}
		if sent == maxOCIAttempts {
			break
		}
		r := v.callOCI(ctx, c.ociKey, c.value, p)
		sent++
		if r.Status == StatusValid {
			return r
		}
		if sent == 1 {
			first = r
		}
	}
	switch {
	case sent == 0:
		return skipped
	case len(c.ociProfiles) == 1:
		return first
	}
	return Result{Status: StatusUnknown, Code: first.Code}
}

type ociAPIKey struct {
	Fingerprint string `json:"fingerprint"`
}

type ociError struct {
	Code string `json:"code"`
}

func (v *Verifier) callOCI(ctx context.Context, key *rsa.PrivateKey, fingerprint string, p secrets.OCIProfile) Result {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := v.newOCIRequest(ctx, key, fingerprint, p)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	status, header, body, err := v.send(req)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	switch status {
	case http.StatusOK:
		// A proxy can also answer 200, so only a key list that holds this
		// fingerprint counts.
		var keys []ociAPIKey
		if err := json.Unmarshal(body, &keys); err != nil {
			return Result{Status: StatusUnknown, Code: CodeRequestFailed}
		}
		for _, k := range keys {
			if strings.EqualFold(k.Fingerprint, fingerprint) {
				return Result{Status: StatusValid, Account: p.Tenancy, Identity: p.User}
			}
		}
		return Result{Status: StatusUnknown, Code: CodeFingerprintNotListed}
	case http.StatusUnauthorized:
		return v.judgeOCIUnauthorized(header, body)
	}
	var e ociError
	if err := json.Unmarshal(body, &e); err != nil || e.Code == "" {
		return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", status)}
	}
	return Result{Status: StatusUnknown, Code: e.Code}
}

// judgeOCIUnauthorized reads a 401. Only OCI's own error body judges the
// key, because a proxy or a gateway can also answer 401. OCI gives the same
// 401 when the client clock is more than five minutes off, so the answer
// counts as rejected only when the response date shows that the clock is
// right; without a date that cannot be ruled out.
func (v *Verifier) judgeOCIUnauthorized(header http.Header, body []byte) Result {
	var e ociError
	if err := json.Unmarshal(body, &e); err != nil || e.Code == "" {
		return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", http.StatusUnauthorized)}
	}
	if e.Code != ociNotAuthenticated {
		return Result{Status: StatusUnknown, Code: e.Code}
	}
	server, err := http.ParseTime(header.Get("Date"))
	switch {
	case err != nil:
		return Result{Status: StatusUnknown, Code: ociNotAuthenticated}
	case v.now().Sub(server).Abs() > ociMaxClockSkew:
		return Result{Status: StatusUnknown, Code: CodeClockSkew}
	}
	return Result{Status: StatusRejected, Code: ociNotAuthenticated}
}

// newOCIRequest builds the signed request. The signing string covers the
// date, the request target, and the host, in the order the OCI SDKs use.
func (v *Verifier) newOCIRequest(ctx context.Context, key *rsa.PrivateKey, fingerprint string, p secrets.OCIProfile) (*http.Request, error) {
	base := strings.TrimSuffix(v.OCIEndpoint, "/")
	if base == "" {
		base = fmt.Sprintf(ociEndpointFormat, p.Region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/"+ociAPIVersion+"/users/"+p.User+"/apiKeys", nil)
	if err != nil {
		return nil, err
	}
	if req.URL.Host == "" {
		return nil, errors.New("verify: OCI endpoint has no host")
	}
	date := v.now().UTC().Format(http.TimeFormat)
	req.Header.Set("Date", date)
	signing := "date: " + date + "\n(request-target): get " + req.URL.RequestURI() + "\nhost: " + req.URL.Host
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", `Signature version="1",headers="date (request-target) host",keyId="`+
		p.Tenancy+"/"+p.User+"/"+fingerprint+`",algorithm="rsa-sha256",signature="`+base64.StdEncoding.EncodeToString(sig)+`"`)
	return req, nil
}
