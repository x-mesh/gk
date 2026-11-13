package verify

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/x-mesh/gk/internal/secrets"
)

var (
	ociTenancy = "ocid1.tenancy.oc1..aaaaaaaa" + strings.Repeat("t", 52)
	ociUser    = "ocid1.user.oc1..aaaaaaaa" + strings.Repeat("u", 52)
	ociUserB   = "ocid1.user.oc1..aaaaaaaa" + strings.Repeat("b", 52)
)

func ociFixtureKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	der, err := base64.StdEncoding.DecodeString(ociFixtureDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKCS1PrivateKey(der)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func ociProfile(user, region string) secrets.OCIProfile {
	return secrets.OCIProfile{Fingerprint: ociFixtureFingerprint, User: user, Tenancy: ociTenancy, Region: region}
}

// The Authorization header must be byte-identical to the OCI SDK's for the
// same key, date, and URL. A check against a signing string that this test
// built itself would only show that the code agrees with the test.
func TestOCISign_MatchesSDKSigner(t *testing.T) {
	key := ociFixtureKey(t)
	now := time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)
	req, err := (&Verifier{Now: func() time.Time { return now }}).newOCIRequest(context.Background(), key, ociFixtureFingerprint, ociProfile(ociUser, "us-ashburn-1"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := req.URL.String(), "https://identity.us-ashburn-1.oci.oraclecloud.com/20160918/users/"+ociUser+"/apiKeys"; got != want {
		t.Errorf("URL = %s, want %s", got, want)
	}
	if got, want := req.Header.Get("Date"), "Thu, 12 Nov 2026 08:30:00 GMT"; got != want {
		t.Errorf("Date = %q, want %q", got, want)
	}
	want := `Signature version="1",headers="date (request-target) host",keyId="` + ociTenancy + "/" + ociUser + "/" + ociFixtureFingerprint +
		`",algorithm="rsa-sha256",signature="` + ociSDKSignature + `"`
	if got := req.Header.Get("Authorization"); got != want {
		t.Errorf("Authorization =\n %s\nwant\n %s", got, want)
	}
}

var ociSignatureRe = regexp.MustCompile(`signature="([^"]+)"`)

// fakeOCI answers ListApiKeys. It checks the signature of every request the
// way OCI does: over the date, request target, and host it received.
type fakeOCI struct {
	t       *testing.T
	pub     *rsa.PublicKey
	calls   atomic.Int32
	respond func(w http.ResponseWriter, user string)
}

func (f *fakeOCI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	m := ociSignatureRe.FindStringSubmatch(r.Header.Get("Authorization"))
	if m == nil {
		f.t.Errorf("request without a signature: %q", r.Header.Get("Authorization"))
		return
	}
	sig, _ := base64.StdEncoding.DecodeString(m[1])
	signing := "date: " + r.Header.Get("Date") + "\n(request-target): " + strings.ToLower(r.Method) + " " + r.URL.RequestURI() + "\nhost: " + r.Host
	sum := sha256.Sum256([]byte(signing))
	if err := rsa.VerifyPKCS1v15(f.pub, crypto.SHA256, sum[:], sig); err != nil {
		f.t.Errorf("signature does not verify over what the server received: %v", err)
	}
	user := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/20160918/users/"), "/apiKeys")
	f.respond(w, user)
}

func ociKeyList(fingerprints ...string) string {
	items := make([]string, len(fingerprints))
	for i, fp := range fingerprints {
		items[i] = fmt.Sprintf(`{"keyId":"k","fingerprint":%q,"userId":"u","lifecycleState":"ACTIVE"}`, fp)
	}
	return "[" + strings.Join(items, ",") + "]"
}

func ociErrorBody(code string) string { return `{"code":"` + code + `","message":"m"}` }

// runOCI verifies one credential against a fake server and returns the
// result and the number of requests the server received.
func runOCI(t *testing.T, profiles []secrets.OCIProfile, respond func(w http.ResponseWriter, user string)) (Result, int) {
	t.Helper()
	key := ociFixtureKey(t)
	fake := &fakeOCI{t: t, pub: &key.PublicKey, respond: respond}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cred := secrets.NewOCICredential(key, profiles)
	got := (&Verifier{OCIEndpoint: srv.URL, Client: srv.Client()}).Verify(context.Background(), []*secrets.Credential{cred, cred})
	if len(got) != 1 {
		t.Fatalf("results = %v, want one result for the one key", got)
	}
	return got[cred.ID()], int(fake.calls.Load())
}

func TestOCI_Outcomes(t *testing.T) {
	reply := func(status int, body string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, _ string) {
			w.WriteHeader(status)
			fmt.Fprint(w, body)
		}
	}
	cases := map[string]struct {
		respond func(http.ResponseWriter, string)
		want    Result
	}{
		"key is listed": {reply(200, ociKeyList("00:11", strings.ToUpper(ociFixtureFingerprint))),
			Result{Status: StatusValid, Account: ociTenancy, Identity: ociUser}},
		"200 without this fingerprint": {reply(200, ociKeyList("00:11")), Result{Status: StatusUnknown, Code: CodeFingerprintNotListed}},
		"200 with an empty list":       {reply(200, "[]"), Result{Status: StatusUnknown, Code: CodeFingerprintNotListed}},
		"200 from a proxy":             {reply(200, "<html>login</html>"), Result{Status: StatusUnknown, Code: CodeRequestFailed}},
		"200 with a JSON object":       {reply(200, `{"fingerprint":"`+ociFixtureFingerprint+`"}`), Result{Status: StatusUnknown, Code: CodeRequestFailed}},
		// OCI's own 401 for the one profile that names the key.
		"401": {reply(401, ociErrorBody("NotAuthenticated")), Result{Status: StatusRejected, Code: "NotAuthenticated"}},
		// A 401 that is not OCI's error body does not judge the key.
		"401 without a body":    {reply(401, ""), Result{Status: StatusUnknown, Code: "http_401"}},
		"401 from a proxy":      {reply(401, "<html>login</html>"), Result{Status: StatusUnknown, Code: "http_401"}},
		"401 with another code": {reply(401, ociErrorBody("SignUpRequired")), Result{Status: StatusUnknown, Code: "SignUpRequired"}},
		// Without the server's date a clock that is off cannot be ruled out.
		"401 without a date": {func(w http.ResponseWriter, _ string) {
			w.Header()["Date"] = nil
			w.WriteHeader(401)
			fmt.Fprint(w, ociErrorBody("NotAuthenticated"))
		}, Result{Status: StatusUnknown, Code: "NotAuthenticated"}},
		"404":                {reply(404, ociErrorBody("NotAuthorizedOrNotFound")), Result{Status: StatusUnknown, Code: "NotAuthorizedOrNotFound"}},
		"429":                {reply(429, ociErrorBody("TooManyRequests")), Result{Status: StatusUnknown, Code: "TooManyRequests"}},
		"500 without a body": {reply(500, ""), Result{Status: StatusUnknown, Code: "http_500"}},
		"401 with a clock that is off": {func(w http.ResponseWriter, _ string) {
			w.Header().Set("Date", time.Now().Add(10*time.Minute).UTC().Format(http.TimeFormat))
			w.WriteHeader(401)
			fmt.Fprint(w, ociErrorBody("NotAuthenticated"))
		}, Result{Status: StatusUnknown, Code: CodeClockSkew}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, calls := runOCI(t, []secrets.OCIProfile{ociProfile(ociUser, "ap-seoul-1")}, tc.respond)
			if got != tc.want {
				t.Errorf("result = %+v, want %+v", got, tc.want)
			}
			// The credential is passed twice and must be checked once.
			if calls != 1 {
				t.Errorf("requests = %d, want 1", calls)
			}
		})
	}
}

// Values that become the host, the path, and the key ID are checked before
// anything is sent.
func TestOCI_ProfileThatCannotBeSentMakesNoRequest(t *testing.T) {
	cases := map[string]struct {
		profile secrets.OCIProfile
		code    string
	}{
		"no region":                 {ociProfile(ociUser, ""), CodeRegionNotFound},
		"region with dots":          {ociProfile(ociUser, "x.evil.example/"), CodeInvalidProfile},
		"region with a host suffix": {ociProfile(ociUser, "ap-seoul-1.evil.example"), CodeInvalidProfile},
		"region in upper case":      {ociProfile(ociUser, "AP-SEOUL-1"), CodeInvalidProfile},
		"user with path characters": {ociProfile(ociUser+"/../x", "ap-seoul-1"), CodeInvalidProfile},
		"user with a quote":         {ociProfile(ociUser+`"`, "ap-seoul-1"), CodeInvalidProfile},
		"user that is a tenancy":    {ociProfile(ociTenancy, "ap-seoul-1"), CodeInvalidProfile},
		"government realm":          {ociProfile(strings.Replace(ociUser, ".oc1.", ".oc2.", 1), "us-langley-1"), CodeUnsupportedRealm},
		"tenancy in another realm": {secrets.OCIProfile{Fingerprint: ociFixtureFingerprint, User: ociUser,
			Tenancy: strings.Replace(ociTenancy, ".oc1.", ".oc19.", 1), Region: "eu-frankfurt-2"}, CodeUnsupportedRealm},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, calls := runOCI(t, []secrets.OCIProfile{tc.profile}, func(http.ResponseWriter, string) {})
			if want := (Result{Status: StatusUnverifiable, Code: tc.code}); got != want {
				t.Errorf("result = %+v, want %+v", got, want)
			}
			if calls != 0 {
				t.Errorf("requests = %d, want 0", calls)
			}
		})
	}
}

func TestOCI_TriesProfileCandidatesInOrder(t *testing.T) {
	byUser := func(valid string) func(http.ResponseWriter, string) {
		return func(w http.ResponseWriter, user string) {
			if user == valid {
				fmt.Fprint(w, ociKeyList(ociFixtureFingerprint))
				return
			}
			w.WriteHeader(401)
			fmt.Fprint(w, ociErrorBody("NotAuthenticated"))
		}
	}
	t.Run("second candidate is right", func(t *testing.T) {
		got, calls := runOCI(t, []secrets.OCIProfile{ociProfile(ociUser, "ap-seoul-1"), ociProfile(ociUserB, "ap-seoul-1")}, byUser(ociUserB))
		if want := (Result{Status: StatusValid, Account: ociTenancy, Identity: ociUserB}); got != want || calls != 2 {
			t.Errorf("result = %+v after %d requests, want %+v after 2", got, calls, want)
		}
	})
	t.Run("a candidate that cannot be sent is skipped", func(t *testing.T) {
		got, calls := runOCI(t, []secrets.OCIProfile{ociProfile(ociUser, ""), ociProfile(ociUserB, "ap-seoul-1")}, byUser(ociUserB))
		if want := (Result{Status: StatusValid, Account: ociTenancy, Identity: ociUserB}); got != want || calls != 1 {
			t.Errorf("result = %+v after %d requests, want %+v after 1", got, calls, want)
		}
	})
	// With more than one candidate a 401 can come from a wrong candidate, so
	// it does not read as a rejected key.
	t.Run("the only candidate that can be sent fails", func(t *testing.T) {
		got, calls := runOCI(t, []secrets.OCIProfile{ociProfile(ociUser, ""), ociProfile(ociUserB, "ap-seoul-1")}, byUser("nobody"))
		if want := (Result{Status: StatusUnknown, Code: "NotAuthenticated"}); got != want || calls != 1 {
			t.Errorf("result = %+v after %d requests, want %+v after 1", got, calls, want)
		}
	})
	t.Run("all candidates fail", func(t *testing.T) {
		var profiles []secrets.OCIProfile
		for i := range 5 {
			profiles = append(profiles, ociProfile(ociUser, fmt.Sprintf("ap-seoul-%d", i+1)))
		}
		got, calls := runOCI(t, profiles, byUser("nobody"))
		if want := (Result{Status: StatusUnknown, Code: "NotAuthenticated"}); got != want || calls != maxOCIAttempts {
			t.Errorf("result = %+v after %d requests, want %+v after %d", got, calls, want, maxOCIAttempts)
		}
	})
}

func TestOCI_RedirectAndNetworkFailure(t *testing.T) {
	key := ociFixtureKey(t)
	cred := secrets.NewOCICredential(key, []secrets.OCIProfile{ociProfile(ociUser, "ap-seoul-1")})

	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redirect.Close()
	got := (&Verifier{OCIEndpoint: redirect.URL, Client: redirect.Client()}).Verify(context.Background(), []*secrets.Credential{cred})
	if reached.Load() {
		t.Fatal("followed a redirect with the signed request")
	}
	if r := got[cred.ID()]; r != (Result{Status: StatusUnknown, Code: "http_302"}) {
		t.Errorf("redirect: result = %+v", r)
	}

	closed := httptest.NewServer(http.NotFoundHandler())
	url := closed.URL
	closed.Close()
	got = (&Verifier{OCIEndpoint: url}).Verify(context.Background(), []*secrets.Credential{cred})
	if r := got[cred.ID()]; r != (Result{Status: StatusUnknown, Code: CodeRequestFailed}) {
		t.Errorf("network failure: result = %+v", r)
	}
}
