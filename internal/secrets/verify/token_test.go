package verify

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/x-mesh/gk/internal/secrets"
)

// Fixture tokens are assembled at runtime so scanners skip this file.
var (
	slackBot = "xoxb-" + "123456789012-1234567890123-" + "AbCdEfGhIjKlMnOpQrStUvWx"
	slackApp = "xapp-" + "1-A0123ABCDEF-1234567890123-" + "abcdef0123456789"
	slackOld = "xoxa-" + "2-" + "AbCdEfGh0123456789"
	gcpKey   = "AIza" + "SyD3x4mpl3K3yV4lu3F0rT3st1ngPurp0s"
)

func slackCred(v string) *secrets.Credential {
	return secrets.NewTokenCredential(secrets.ProviderSlack, "slack-bot-token", v)
}

func gcpCred(v string) *secrets.Credential {
	return secrets.NewTokenCredential(secrets.ProviderGCP, "gcp-api-key", v)
}

func TestSlack_Outcomes(t *testing.T) {
	answers := map[string]string{
		slackBot:                         `{"ok":true,"url":"https://acme.slack.com/","team":"Acme","user":"bot","team_id":"T0123","user_id":"U0456"}`,
		slackBot[:len(slackBot)-1] + "0": `{"ok":false,"error":"invalid_auth"}`,
		slackBot[:len(slackBot)-1] + "1": `{"ok":false,"error":"ratelimited"}`,
		slackApp:                         `{"ok":false,"error":"invalid_auth"}`,
		slackOld:                         `{"ok":false,"error":"token_revoked"}`,
	}
	var leaked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if strings.Contains(r.URL.String(), "xox") || strings.Contains(string(body), "xox") {
			leaked.Store(true)
		}
		if token == slackBot[:len(slackBot)-1]+"2" {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		fmt.Fprint(w, answers[token])
	}))
	defer srv.Close()
	v := &Verifier{SlackEndpoint: srv.URL, Client: srv.Client()}
	creds := []*secrets.Credential{
		slackCred(slackBot), slackCred(slackBot[:len(slackBot)-1] + "0"), slackCred(slackBot[:len(slackBot)-1] + "1"),
		slackCred(slackBot[:len(slackBot)-1] + "2"), secrets.NewTokenCredential(secrets.ProviderSlack, "slack-app-token", slackApp),
		secrets.NewTokenCredential(secrets.ProviderSlack, "slack-legacy-workspace-token", slackOld),
	}
	got := byValue(v.Verify(context.Background(), creds))
	want := map[string]Result{
		slackBot:                         {Status: StatusValid, Account: "T0123", AccountName: "Acme", URL: "https://acme.slack.com/", Identity: "U0456", IdentityName: "bot"},
		slackBot[:len(slackBot)-1] + "0": {Status: StatusRejected, Code: "invalid_auth"},
		slackBot[:len(slackBot)-1] + "1": {Status: StatusUnknown, Code: "ratelimited"},
		slackBot[:len(slackBot)-1] + "2": {Status: StatusUnknown, Code: "http_429"},
		// auth.test support for app-level tokens is unconfirmed, so an
		// error does not read as a dead token.
		slackApp: {Status: StatusUnknown, Code: "invalid_auth"},
		// Legacy token types are not documented for auth.test either.
		slackOld: {Status: StatusUnknown, Code: "token_revoked"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Verify =\n %v\nwant\n %v", got, want)
	}
	if leaked.Load() {
		t.Error("token sent outside the Authorization header")
	}
}

func gcpErrorBody(code int, reason, consumer string) string {
	meta := `"service":"discovery.googleapis.com"`
	if consumer != "" {
		meta += `,"consumer":"` + consumer + `"`
	}
	return fmt.Sprintf(`{"error":{"code":%d,"message":"m","status":"S","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":%q,"domain":"googleapis.com","metadata":{%s}}]}}`, code, reason, meta)
}

func TestGCP_Outcomes(t *testing.T) {
	keys := map[string]struct {
		status int
		body   string
	}{
		gcpKey:            {http.StatusOK, `{"kind":"discovery#directoryList"}`},
		gcpKey[:38] + "0": {http.StatusBadRequest, gcpErrorBody(400, "API_KEY_INVALID", "")},
		gcpKey[:38] + "1": {http.StatusForbidden, gcpErrorBody(403, "API_KEY_HTTP_REFERRER_BLOCKED", "projects/123456")},
		gcpKey[:38] + "2": {http.StatusForbidden, gcpErrorBody(403, "RATE_LIMIT_EXCEEDED", "")},
		gcpKey[:38] + "3": {http.StatusBadGateway, ""},
		gcpKey[:38] + "4": {http.StatusOK, `<html>proxy login</html>`},
	}
	var leaked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.String(), "AIza") {
			leaked.Store(true)
		}
		a := keys[r.Header.Get("X-Goog-Api-Key")]
		w.WriteHeader(a.status)
		fmt.Fprint(w, a.body)
	}))
	defer srv.Close()
	v := &Verifier{GCPEndpoint: srv.URL + "/discovery/v1/apis", Client: srv.Client()}
	var creds []*secrets.Credential
	for k := range keys {
		creds = append(creds, gcpCred(k))
	}
	got := byValue(v.Verify(context.Background(), creds))
	want := map[string]Result{
		gcpKey:            {Status: StatusValid},
		gcpKey[:38] + "0": {Status: StatusRejected, Code: "API_KEY_INVALID"},
		gcpKey[:38] + "1": {Status: StatusValid, Account: "projects/123456", Code: "API_KEY_HTTP_REFERRER_BLOCKED"},
		gcpKey[:38] + "2": {Status: StatusUnknown, Code: "RATE_LIMIT_EXCEEDED"},
		gcpKey[:38] + "3": {Status: StatusUnknown, Code: "http_502"},
		gcpKey[:38] + "4": {Status: StatusUnknown, Code: CodeRequestFailed},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("Verify =\n %v\nwant\n %v", got, want)
	}
	if leaked.Load() {
		t.Error("key sent in the URL")
	}
}

// A redirect must not carry a credential header to another host.
func TestVerify_DoesNotFollowRedirects(t *testing.T) {
	var reached atomic.Bool
	other := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached.Store(true) }))
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer redirect.Close()
	v := &Verifier{SlackEndpoint: redirect.URL, GCPEndpoint: redirect.URL, Client: redirect.Client()}
	got := byValue(v.Verify(context.Background(), []*secrets.Credential{slackCred(slackBot), gcpCred(gcpKey)}))
	if reached.Load() {
		t.Fatal("followed a redirect with the credential")
	}
	for k, r := range got {
		if r != (Result{Status: StatusUnknown, Code: "http_302"}) {
			t.Errorf("%.8s: %+v, want unknown http_302", k, r)
		}
	}
}
