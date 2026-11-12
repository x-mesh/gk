package secrets

import (
	"regexp"
	"slices"
	"strings"
)

// AWSCredential is an AWS access key ID with the secret access key candidates
// and session token found next to it. It holds raw credential values for
// live verification, so every formatter and encoder sees only the masked key
// ID: a finding that carries one can be printed or marshaled without leaking.
type AWSCredential struct {
	keyID   string
	secrets []string
	token   string
}

// KeyID returns the raw access key ID.
func (c *AWSCredential) KeyID() string { return c.keyID }

// SecretCandidates returns the raw secret access key candidates, nearest first.
func (c *AWSCredential) SecretCandidates() []string { return slices.Clone(c.secrets) }

// SessionToken returns the raw session token, or "" when none was found.
func (c *AWSCredential) SessionToken() string { return c.token }

// MaskedKeyID shows the key ID prefix and its last four characters.
func (c *AWSCredential) MaskedKeyID() string {
	if len(c.keyID) < 8 {
		return strings.Repeat("*", len(c.keyID))
	}
	return c.keyID[:4] + "..." + c.keyID[len(c.keyID)-4:]
}

func (c *AWSCredential) String() string   { return "AWSCredential(" + c.MaskedKeyID() + ")" }
func (c *AWSCredential) GoString() string { return c.String() }

// MarshalJSON keeps raw values out of any encoded finding.
func (c *AWSCredential) MarshalJSON() ([]byte, error) {
	return []byte(`"` + c.MaskedKeyID() + `"`), nil
}

// NewAWSCredential builds a credential directly; scanners use PairAWS.
func NewAWSCredential(keyID string, secretCandidates []string, token string) *AWSCredential {
	return &AWSCredential{keyID: keyID, secrets: slices.Clone(secretCandidates), token: token}
}

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
func PairAWS(lines []string, idx int, keyID string) *AWSCredential {
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
	return &AWSCredential{keyID: keyID, secrets: secrets, token: token}
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
