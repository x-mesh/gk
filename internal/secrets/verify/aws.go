package verify

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAWSEndpoint = "https://sts.amazonaws.com/"
	// The global STS endpoint signs in us-east-1.
	signingRegion  = "us-east-1"
	signingService = "sts"
	requestBody    = "Action=GetCallerIdentity&Version=2011-06-15"
	// maxAttemptsPerKey bounds the STS calls for one key ID after the
	// candidates from every finding of that key are merged.
	maxAttemptsPerKey = 3
)

// checkAWS tries the secret candidates in order. A wrong secret fails with
// SignatureDoesNotMatch, so the next candidate is tried; any other answer
// decides the key.
func (v *Verifier) checkAWS(ctx context.Context, c *mergedCred) Result {
	switch {
	case !strings.HasPrefix(c.value, "AKIA") && !strings.HasPrefix(c.value, "ASIA"):
		return Result{Status: StatusUnverifiable, Code: CodeUnsupportedKeyType}
	case len(c.secrets) == 0:
		return Result{Status: StatusUnverifiable, Code: CodeSecretNotFound}
	case strings.HasPrefix(c.value, "ASIA") && c.token == "":
		return Result{Status: StatusUnverifiable, Code: CodeSessionTokenNotFound}
	}
	var last Result
	for _, secret := range c.secrets[:min(len(c.secrets), maxAttemptsPerKey)] {
		last = v.callSTS(ctx, c.value, secret, c.token)
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

func (v *Verifier) callSTS(ctx context.Context, keyID, secret, token string) Result {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := v.newSTSRequest(ctx, keyID, secret, token)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	status, body, err := v.do(req)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if status == http.StatusOK {
		var ok callerIdentityResponse
		if err := xml.Unmarshal(body, &ok); err != nil || ok.Account == "" {
			return Result{Status: StatusUnknown, Code: CodeRequestFailed}
		}
		return Result{Status: StatusValid, Account: ok.Account, ARN: ok.ARN}
	}
	var e errorResponse
	if err := xml.Unmarshal(body, &e); err != nil || e.Code == "" {
		return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", status)}
	}
	switch e.Code {
	case "InvalidClientTokenId", "ExpiredToken":
		return Result{Status: StatusRejected, Code: e.Code}
	default:
		return Result{Status: StatusUnknown, Code: e.Code}
	}
}

func (v *Verifier) newSTSRequest(ctx context.Context, keyID, secret, token string) (*http.Request, error) {
	endpoint := v.AWSEndpoint
	if endpoint == "" {
		endpoint = defaultAWSEndpoint
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(requestBody))
	if err != nil {
		return nil, err
	}
	if req.URL.Host == "" {
		return nil, errors.New("verify: AWS endpoint has no host")
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
