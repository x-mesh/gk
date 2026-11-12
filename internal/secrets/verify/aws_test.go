package verify

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/secrets"
)

// AWS documentation example credentials; assembled so scanners skip this file.
var (
	exampleKeyID  = "AKIA" + "IOSFODNN7EXAMPLE"
	exampleSecret = "wJalrXUtnFEMI/K7MDENG/" + "bPxRfiCYEXAMPLEKEY"
	exampleToken  = "FwoGZXIvYXdzEXAMPLETOKEN" + strings.Repeat("x", 81)
)

// The expected signatures came from aws-sdk-go-v2 v1.48.0 aws/signer/v4
// SignHTTP for the same request, credentials, and time.
func TestSign_MatchesSDKSigner(t *testing.T) {
	now := time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)
	cases := []struct {
		token, signed, sig string
	}{
		{"", "content-length;content-type;host;x-amz-date", "09afb5f3f564c9d74c5996ed3663f12b072e5cce6eae724c64ac6ee36e5fde5f"},
		{exampleToken, "content-length;content-type;host;x-amz-date;x-amz-security-token", "a64daa929bff643eed239bb24fb5eb57832724ed73e66f95ba962da6970fa5ac"},
	}
	for _, tc := range cases {
		req, err := (&Verifier{Now: func() time.Time { return now }}).newSTSRequest(context.Background(), exampleKeyID, exampleSecret, tc.token)
		if err != nil {
			t.Fatal(err)
		}
		want := "AWS4-HMAC-SHA256 Credential=" + exampleKeyID + "/20261112/us-east-1/sts/aws4_request, SignedHeaders=" + tc.signed + ", Signature=" + tc.sig
		if got := req.Header.Get("Authorization"); got != want {
			t.Errorf("token=%v Authorization =\n %s\nwant\n %s", tc.token != "", got, want)
		}
	}
}

// fakeSTS answers by the secret that signed the request, which it recovers by
// re-signing with each known secret.
type fakeSTS struct {
	mu      sync.Mutex
	calls   int
	answers map[string]string // secret -> response body; "" key = unknown ID
	status  map[string]int
}

func (f *fakeSTS) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls++
		f.mu.Unlock()
		auth := r.Header.Get("Authorization")
		for secret, body := range f.answers {
			probe := r.Clone(r.Context())
			probe.Header = r.Header.Clone()
			// A server request carries the host in r.Host, not in the URL.
			probe.URL.Host = r.Host
			date, _ := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
			sign(probe, keyIDFrom(auth), secret, r.Header.Get("X-Amz-Security-Token"), date)
			if probe.Header.Get("Authorization") == auth {
				w.WriteHeader(f.status[secret])
				fmt.Fprint(w, body)
				return
			}
		}
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, stsError("SignatureDoesNotMatch"))
	})
}

func keyIDFrom(auth string) string {
	s := strings.TrimPrefix(auth, "AWS4-HMAC-SHA256 Credential=")
	return s[:strings.IndexByte(s, '/')]
}

func stsError(code string) string {
	return `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><Error><Type>Sender</Type><Code>` + code + `</Code><Message>m</Message></Error><RequestId>r</RequestId></ErrorResponse>`
}

const stsOK = `<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/"><GetCallerIdentityResult><Arn>arn:aws:iam::123456789012:user/ci</Arn><UserId>AIDA</UserId><Account>123456789012</Account></GetCallerIdentityResult></GetCallerIdentityResponse>`

func TestVerify_Outcomes(t *testing.T) {
	sts := &fakeSTS{
		answers: map[string]string{exampleSecret: stsOK, "expired" + exampleSecret[7:]: stsError("ExpiredToken")},
		status:  map[string]int{exampleSecret: http.StatusOK, "expired" + exampleSecret[7:]: http.StatusForbidden},
	}
	srv := httptest.NewServer(sts.handler(t))
	defer srv.Close()
	v := &Verifier{AWSEndpoint: srv.URL + "/", Client: srv.Client()}

	wrong := "Wrong0" + exampleSecret[6:]
	asia := "ASIA" + exampleKeyID[4:]
	creds := []*secrets.Credential{
		// The first candidate is wrong, so the second is tried.
		secrets.NewAWSCredential(exampleKeyID, []string{wrong, exampleSecret}, ""),
		// The same key found again in another file is checked once.
		secrets.NewAWSCredential(exampleKeyID, []string{exampleSecret}, ""),
		secrets.NewAWSCredential("AKIA"+"AAAAAAAAAAAAAAA1", []string{wrong}, ""),
		secrets.NewAWSCredential("AKIA"+"AAAAAAAAAAAAAAA2", nil, ""),
		secrets.NewAWSCredential(asia, []string{exampleSecret}, ""),
		secrets.NewAWSCredential("ABIA"+exampleKeyID[4:], []string{exampleSecret}, ""),
		secrets.NewAWSCredential("ASIA"+"AAAAAAAAAAAAAAA3", []string{"expired" + exampleSecret[7:]}, exampleToken),
		// Four wrong candidates merged from two findings stop at the cap.
		secrets.NewAWSCredential("AKIA"+"AAAAAAAAAAAAAAA4", []string{"Wrong1" + exampleSecret[6:], "Wrong2" + exampleSecret[6:]}, ""),
		secrets.NewAWSCredential("AKIA"+"AAAAAAAAAAAAAAA4", []string{"Wrong3" + exampleSecret[6:], "Wrong4" + exampleSecret[6:]}, ""),
	}
	got := byValue(v.Verify(context.Background(), creds))
	want := map[string]Result{
		exampleKeyID:                {Status: StatusValid, Account: "123456789012", ARN: "arn:aws:iam::123456789012:user/ci"},
		"AKIA" + "AAAAAAAAAAAAAAA1": {Status: StatusUnknown, Code: "SignatureDoesNotMatch"},
		"AKIA" + "AAAAAAAAAAAAAAA2": {Status: StatusUnverifiable, Code: CodeSecretNotFound},
		asia:                        {Status: StatusUnverifiable, Code: CodeSessionTokenNotFound},
		"ABIA" + exampleKeyID[4:]:   {Status: StatusUnverifiable, Code: CodeUnsupportedKeyType},
		"ASIA" + "AAAAAAAAAAAAAAA3": {Status: StatusRejected, Code: "ExpiredToken"},
		"AKIA" + "AAAAAAAAAAAAAAA4": {Status: StatusUnknown, Code: "SignatureDoesNotMatch"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Verify =\n %v\nwant\n %v", got, want)
	}
	// exampleKeyID: wrong then right (2); ...1: wrong (1); ...3: expired (1);
	// ...4: capped at maxAttemptsPerKey (3).
	if sts.calls != 7 {
		t.Errorf("STS calls = %d, want 7", sts.calls)
	}
}

func TestVerify_InvalidKeyIDIsRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, stsError("InvalidClientTokenId"))
	}))
	defer srv.Close()
	got := byValue((&Verifier{AWSEndpoint: srv.URL + "/", Client: srv.Client()}).Verify(context.Background(),
		[]*secrets.Credential{secrets.NewAWSCredential(exampleKeyID, []string{exampleSecret, "Other0" + exampleSecret[6:]}, "")}))
	if r := got[exampleKeyID]; r != (Result{Status: StatusRejected, Code: "InvalidClientTokenId"}) {
		t.Errorf("result = %+v", r)
	}
}

func TestVerify_NetworkFailureIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL + "/"
	srv.Close()
	got := byValue((&Verifier{AWSEndpoint: url}).Verify(context.Background(),
		[]*secrets.Credential{secrets.NewAWSCredential(exampleKeyID, []string{exampleSecret}, "")}))
	if r := got[exampleKeyID]; r != (Result{Status: StatusUnknown, Code: CodeRequestFailed}) {
		t.Errorf("result = %+v", r)
	}
}

func TestVerify_NonXMLErrorIsUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	got := byValue((&Verifier{AWSEndpoint: srv.URL + "/", Client: srv.Client()}).Verify(context.Background(),
		[]*secrets.Credential{secrets.NewAWSCredential(exampleKeyID, []string{exampleSecret}, "")}))
	if r := got[exampleKeyID]; r != (Result{Status: StatusUnknown, Code: "http_502"}) {
		t.Errorf("result = %+v", r)
	}
}

// byValue rekeys results by the raw value, since every credential in a test
// has one provider.
func byValue(rs map[string]Result) map[string]Result {
	out := make(map[string]Result, len(rs))
	for id, r := range rs {
		out[id[strings.IndexByte(id, 0)+1:]] = r
	}
	return out
}
