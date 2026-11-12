package secrets

import (
	"regexp"
	"slices"
	"strings"
)

// Provider names the issuer that can check a Credential.
const (
	ProviderAWS   = "aws"
	ProviderSlack = "slack"
	ProviderGCP   = "gcp"
)

// Credential is a raw credential a scan found, kept for live verification:
// an AWS access key ID with the secret candidates and session token next to
// it, a Slack token, or a GCP API key. Every formatter and encoder sees only
// the masked value, so a finding that carries one can be printed or marshaled
// without leaking.
type Credential struct {
	provider string
	kind     string
	value    string
	secrets  []string
	token    string
}

// Provider returns one of the Provider constants.
func (c *Credential) Provider() string { return c.provider }

// Kind returns the scanner rule ID that found the credential.
func (c *Credential) Kind() string { return c.kind }

// Value returns the raw AWS access key ID, Slack token, or GCP API key.
func (c *Credential) Value() string { return c.value }

// ID identifies the credential across findings: the same value found in
// several files or commits has one ID. It contains the raw value.
func (c *Credential) ID() string { return c.provider + "\x00" + c.value }

// SecretCandidates returns the raw AWS secret access key candidates, nearest
// first.
func (c *Credential) SecretCandidates() []string { return slices.Clone(c.secrets) }

// SessionToken returns the raw AWS session token, or "" when none was found.
func (c *Credential) SessionToken() string { return c.token }

// Masked shows the value's first and last four characters.
func (c *Credential) Masked() string {
	if len(c.value) < 12 {
		return strings.Repeat("*", len(c.value))
	}
	return c.value[:4] + "..." + c.value[len(c.value)-4:]
}

func (c *Credential) String() string   { return "Credential(" + c.provider + " " + c.Masked() + ")" }
func (c *Credential) GoString() string { return c.String() }

// MarshalJSON keeps raw values out of any encoded finding.
func (c *Credential) MarshalJSON() ([]byte, error) {
	return []byte(`"` + c.Masked() + `"`), nil
}

// NewAWSCredential builds an AWS credential directly; scanners use PairAWS.
func NewAWSCredential(keyID string, secretCandidates []string, token string) *Credential {
	return &Credential{provider: ProviderAWS, kind: "aws-access-token", value: keyID, secrets: slices.Clone(secretCandidates), token: token}
}

// NewTokenCredential builds a single-value credential directly; scanners use
// TokenCredential.
func NewTokenCredential(provider, kind, value string) *Credential {
	return &Credential{provider: provider, kind: kind, value: value}
}

// tokenRules maps the scanner rules whose match is checked as one bearer
// value to the provider and the characters a full token can contain.
// slack-webhook-url and slack-config-refresh-token are absent on purpose:
// the only checks for them post a message or rotate the token.
var tokenRules = map[string]struct {
	provider string
	char     func(byte) bool
}{
	"slack-bot-token":              {ProviderSlack, isSlackTokenChar},
	"slack-legacy-bot-token":       {ProviderSlack, isSlackTokenChar},
	"slack-user-token":             {ProviderSlack, isSlackTokenChar},
	"slack-legacy-token":           {ProviderSlack, isSlackTokenChar},
	"slack-legacy-workspace-token": {ProviderSlack, isSlackTokenChar},
	"slack-app-token":              {ProviderSlack, isSlackTokenChar},
	"slack-config-access-token":    {ProviderSlack, isSlackTokenChar},
	"gcp-api-key":                  {ProviderGCP, isGCPKeyChar},
}

// TokenCredential returns the credential for a finding of a token rule, or
// nil for any other rule. Several Slack rules bound the length of the token
// tail, so the match can stop inside a longer token, and a truncated token
// would read as invalid. The value is therefore extended to the end of the
// token's character run on the raw line.
func TokenCredential(ruleID, line, match string) *Credential {
	r, ok := tokenRules[ruleID]
	if !ok || match == "" {
		return nil
	}
	value := match
	if i := strings.Index(line, match); i >= 0 {
		end := i + len(match)
		for end < len(line) && r.char(line[end]) {
			end++
		}
		value = line[i:end]
	}
	// slack-legacy-workspace-token also matches xoxr-, which looks like a
	// refresh token; Slack does not document it, so it is not sent.
	if strings.HasPrefix(value, "xoxr-") {
		return nil
	}
	return &Credential{provider: r.provider, kind: ruleID, value: value}
}

func isAlnum(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

func isSlackTokenChar(b byte) bool { return isAlnum(b) || b == '-' }
func isGCPKeyChar(b byte) bool     { return isAlnum(b) || b == '-' || b == '_' }

// awsPairWindow is how many lines above and below the key ID are searched.
// Credential files, .env files, and SDK config literals keep the pair on the
// same or adjacent lines.
const awsPairWindow = 5

// awsMaxSecretCandidates bounds the candidates kept for one finding.
const awsMaxSecretCandidates = 3

var (
	awsSecretCandidate = regexp.MustCompile(`(?:^|[^A-Za-z0-9/+])([A-Za-z0-9/+]{40})(?:[^A-Za-z0-9/+=]|$)`)
	awsSessionToken    = regexp.MustCompile(`[A-Za-z0-9/+=]{100,}`)
)

// PairAWS collects the secret access key candidates and the session token
// around lines[idx], which holds keyID. It searches the raw lines instead of
// other findings because the generic-api-key finding for a secret on the
// key ID's own line is dropped as shadowed. Candidates on lines that mention
// "secret" come first, then nearer lines.
func PairAWS(lines []string, idx int, keyID string) *Credential {
	type cand struct {
		value   string
		keyword bool
		dist    int
	}
	var cands []cand
	token, tokenDist := "", -1
	lo, hi := max(idx-awsPairWindow, 0), min(idx+awsPairWindow, len(lines)-1)
	for i := lo; i <= hi; i++ {
		line := lines[i]
		lower := strings.ToLower(line)
		dist := abs(i - idx)
		for _, m := range awsSecretCandidate.FindAllStringSubmatch(line, -1) {
			if v := m[1]; looksLikeAWSSecret(v) {
				cands = append(cands, cand{value: v, keyword: strings.Contains(lower, "secret"), dist: dist})
			}
		}
		if strings.Contains(lower, "token") && (tokenDist < 0 || dist < tokenDist) {
			if t := awsSessionToken.FindString(line); t != "" {
				token, tokenDist = t, dist
			}
		}
	}
	slices.SortStableFunc(cands, func(a, b cand) int {
		if a.keyword != b.keyword {
			if a.keyword {
				return -1
			}
			return 1
		}
		return a.dist - b.dist
	})
	var secrets []string
	for _, c := range cands {
		if !slices.Contains(secrets, c.value) {
			secrets = append(secrets, c.value)
		}
		if len(secrets) == awsMaxSecretCandidates {
			break
		}
	}
	return &Credential{provider: ProviderAWS, kind: "aws-access-token", value: keyID, secrets: secrets, token: token}
}

// looksLikeAWSSecret drops 40-character runs that are not random base64,
// such as a lowercase hex commit SHA.
func looksLikeAWSSecret(s string) bool {
	return strings.ContainsFunc(s, isUpper) && strings.ContainsFunc(s, isLower)
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func isLower(r rune) bool { return r >= 'a' && r <= 'z' }

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
