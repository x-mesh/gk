package verify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const defaultSlackEndpoint = "https://slack.com/api/auth.test"

// slackRejected are the auth.test errors that judge the token itself.
// Others, such as not_authed or ratelimited, say nothing about it.
var slackRejected = map[string]bool{
	"invalid_auth":     true,
	"token_revoked":    true,
	"token_expired":    true,
	"account_inactive": true,
}

// slackDecisivePrefixes are the bot and user token types that Slack
// documents. For app-level, xoxe, and legacy tokens it is unconfirmed that
// auth.test judges them, so an error there stays unknown instead of reading
// as a dead token.
var slackDecisivePrefixes = []string{"xoxb-", "xoxp-"}

type slackAuthTest struct {
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	TeamID string `json:"team_id"`
	Team   string `json:"team"`
	URL    string `json:"url"`
	UserID string `json:"user_id"`
	User   string `json:"user"`
}

func (v *Verifier) checkSlack(ctx context.Context, c *mergedCred) Result {
	endpoint := v.SlackEndpoint
	if endpoint == "" {
		endpoint = defaultSlackEndpoint
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	req.Header.Set("Authorization", "Bearer "+c.value)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	status, body, err := v.do(req)
	if err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if status != http.StatusOK {
		return Result{Status: StatusUnknown, Code: fmt.Sprintf("http_%d", status)}
	}
	var r slackAuthTest
	if err := json.Unmarshal(body, &r); err != nil {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if r.OK {
		return Result{Status: StatusValid, Account: r.TeamID, AccountName: r.Team, URL: r.URL, Identity: r.UserID, IdentityName: r.User}
	}
	if r.Error == "" {
		return Result{Status: StatusUnknown, Code: CodeRequestFailed}
	}
	if slackRejected[r.Error] && slackDecisive(c.value) {
		return Result{Status: StatusRejected, Code: r.Error}
	}
	return Result{Status: StatusUnknown, Code: r.Error}
}

func slackDecisive(token string) bool {
	for _, p := range slackDecisivePrefixes {
		if strings.HasPrefix(token, p) {
			return true
		}
	}
	return false
}
