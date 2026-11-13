package secrets

import (
	"crypto/md5"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"regexp"
	"slices"
	"strings"
)

// OCIProfile is the identity half of an OCI API signing key: the values an
// OCI config, Terraform variable file, or environment file keeps next to the
// key fingerprint. None of them is a secret. Region is "" when none was
// found near the fingerprint.
type OCIProfile struct {
	Fingerprint string
	User        string
	Tenancy     string
	Region      string
}

const (
	// ociProfileWindow is how many lines above and below a fingerprint are
	// searched. A config profile that inherits tenancy and region from
	// [DEFAULT] keeps them within a few lines of its own fingerprint.
	ociProfileWindow = 10
	// OCIMaxSendable bounds the profiles that can be sent for one key, and
	// so the requests a verifier spends on it.
	OCIMaxSendable = 3
	// ociFingerprintColons is the number of colons in an MD5 fingerprint.
	ociFingerprintColons = 15
	// ociMaxPEMBytes is above the PEM size of an 8192-bit RSA key, so a
	// larger block is not parsed.
	ociMaxPEMBytes = 16 << 10
	// OCI requires at least 2048 bits. The upper bound keeps signing cheap.
	ociMinKeyBits = 2048
	ociMaxKeyBits = 8192
)

var (
	ociFingerprintRe = regexp.MustCompile(`(?i)(?:^|[^0-9a-f:])((?:[0-9a-f]{2}:){15}[0-9a-f]{2})(?:[^0-9a-f:]|$)`)
	ociUserRe        = regexp.MustCompile(`ocid1\.user\.oc[0-9]{1,2}\.\.[a-z0-9]{20,100}`)
	ociTenancyRe     = regexp.MustCompile(`ocid1\.tenancy\.oc[0-9]{1,2}\.\.[a-z0-9]{20,100}`)
	ociRegionRe      = regexp.MustCompile(`(?:^|[^a-z0-9-])([a-z]{2,3}(?:-[a-z]+){1,3}-[0-9]{1,2})(?:[^a-z0-9-]|$)`)
)

// FindOCIProfiles returns the OCI profile candidates in text, which is a
// file's content or a raw diff. A fingerprint anchors each profile; the user
// OCID, tenancy OCID, and region come from the lines around it. When several
// values are in range, the nearest combinations come first, bounded for
// each fingerprint occurrence as AppendOCIProfile describes. A region counts
// only on a
// line that mentions "region", so another hyphenated word is not taken for
// one.
//
// In a diff, removed lines count too. The pair is made by fingerprint, so a
// profile that a commit deleted still names its key.
func FindOCIProfiles(text string) []OCIProfile {
	if !strings.Contains(text, "ocid1.") {
		return nil
	}
	lines := strings.Split(text, "\n")
	var out []OCIProfile
	for i, line := range lines {
		if strings.Count(line, ":") < ociFingerprintColons {
			continue
		}
		for _, m := range ociFingerprintRe.FindAllStringSubmatch(line, -1) {
			out = append(out, ociProfilesAround(lines, i, strings.ToLower(m[1]))...)
		}
	}
	return out
}

type ociCandidate struct {
	value string
	dist  int
}

func ociProfilesAround(lines []string, idx int, fingerprint string) []OCIProfile {
	// A diff holds many files; the window must not join values of two files.
	lo, hi := idx, idx
	for lo > 0 && idx-(lo-1) <= ociProfileWindow && !isDiffFileHeader(lines[lo-1]) {
		lo--
	}
	for hi < len(lines)-1 && (hi+1)-idx <= ociProfileWindow && !isDiffFileHeader(lines[hi+1]) {
		hi++
	}
	var users, tenancies, regions []ociCandidate
	for i := lo; i <= hi; i++ {
		dist := abs(i - idx)
		for _, v := range ociUserRe.FindAllString(lines[i], -1) {
			users = addOCICandidate(users, v, dist)
		}
		for _, v := range ociTenancyRe.FindAllString(lines[i], -1) {
			tenancies = addOCICandidate(tenancies, v, dist)
		}
		if strings.Contains(strings.ToLower(lines[i]), "region") {
			for _, m := range ociRegionRe.FindAllStringSubmatch(lines[i], -1) {
				regions = addOCICandidate(regions, m[1], dist)
			}
		}
	}
	if len(users) == 0 || len(tenancies) == 0 {
		return nil
	}
	if len(regions) == 0 {
		regions = []ociCandidate{{}}
	}
	type combo struct {
		profile OCIProfile
		dist    int
	}
	var combos []combo
	for _, u := range nearestOCICandidates(users) {
		for _, t := range nearestOCICandidates(tenancies) {
			for _, r := range nearestOCICandidates(regions) {
				combos = append(combos, combo{
					profile: OCIProfile{Fingerprint: fingerprint, User: u.value, Tenancy: t.value, Region: r.value},
					dist:    u.dist + t.dist + r.dist,
				})
			}
		}
	}
	slices.SortStableFunc(combos, func(a, b combo) int { return a.dist - b.dist })
	var out []OCIProfile
	for _, c := range combos {
		out = AppendOCIProfile(out, c.profile)
	}
	return out
}

// Reasons a profile cannot be sent to OCI.
const (
	OCIProfileInvalid          = "invalid_profile"
	OCIProfileUnsupportedRealm = "unsupported_realm"
	OCIProfileNoRegion         = "region_not_found"
)

// ociRealm is the commercial realm, the only one that is checked. Each other
// realm has its own domain, and none of them could be tried against a real
// endpoint.
const ociRealm = "oc1"

var (
	ociStrictOCIDRe   = regexp.MustCompile(`^ocid1\.(user|tenancy)\.(oc[0-9]{1,2})\.\.[a-z0-9]{20,100}$`)
	ociStrictRegionRe = regexp.MustCompile(`^[a-z]{2,3}(-[a-z]+){1,3}-[0-9]{1,2}$`)
)

// Problem returns the reason the profile cannot be sent to OCI, or "". The
// values come from repository text and become the request host, path, and
// key ID, so the patterns here are anchored. A verifier must call Problem on
// each profile right before it builds the request, whatever filtered the
// list before.
func (p OCIProfile) Problem() string {
	user := ociStrictOCIDRe.FindStringSubmatch(p.User)
	tenancy := ociStrictOCIDRe.FindStringSubmatch(p.Tenancy)
	switch {
	case user == nil || tenancy == nil || user[1] != "user" || tenancy[1] != "tenancy":
		return OCIProfileInvalid
	case user[2] != ociRealm || tenancy[2] != ociRealm:
		return OCIProfileUnsupportedRealm
	case p.Region == "":
		return OCIProfileNoRegion
	case !ociStrictRegionRe.MatchString(p.Region):
		return OCIProfileInvalid
	}
	return ""
}

// AppendOCIProfile adds p to the candidates for one key and keeps the list
// bounded: at most OCIMaxSendable profiles that can be sent, and at most one
// that cannot. Only sendable profiles count toward the limit, because a
// profile that cannot be sent would otherwise push out one that can. The
// one unsendable profile stays for two reasons: it carries the reason when
// nothing can be sent, and it keeps a key that has other candidates from
// looking like a key with a single profile.
func AppendOCIProfile(list []OCIProfile, p OCIProfile) []OCIProfile {
	if slices.Contains(list, p) {
		return list
	}
	sendable := 0
	for _, q := range list {
		if q.Problem() == "" {
			sendable++
		}
	}
	if p.Problem() == "" {
		if sendable >= OCIMaxSendable {
			return list
		}
	} else if len(list)-sendable >= 1 {
		return list
	}
	return append(list, p)
}

func isDiffFileHeader(line string) bool { return strings.HasPrefix(line, "diff --git ") }

// addOCICandidate keeps one entry for each value, at its nearest distance.
func addOCICandidate(cands []ociCandidate, value string, dist int) []ociCandidate {
	for i := range cands {
		if cands[i].value == value {
			cands[i].dist = min(cands[i].dist, dist)
			return cands
		}
	}
	return append(cands, ociCandidate{value: value, dist: dist})
}

// nearestOCICandidates returns the two nearest values: enough to cover a
// profile that sits between its own values and the ones it inherits.
func nearestOCICandidates(cands []ociCandidate) []ociCandidate {
	sorted := slices.Clone(cands)
	slices.SortStableFunc(sorted, func(a, b ociCandidate) int { return a.dist - b.dist })
	return sorted[:min(len(sorted), 2)]
}

// BindOCI pairs a private-key candidate with the profiles that name its
// fingerprint and returns the OCI credential, or nil when the PEM is not an
// unencrypted RSA key of an accepted size or no profile names it. The
// fingerprint is computed from the key, so the pairing does not depend on
// where the profile and the key sit: they can be in different files or
// commits. The profiles are kept in the order given, bounded as
// AppendOCIProfile describes.
func BindOCI(candidate *Credential, profiles []OCIProfile) *Credential {
	if candidate == nil || candidate.pem == "" || len(profiles) == 0 {
		return nil
	}
	key := parseOCIKey(candidate.pem)
	if key == nil {
		return nil
	}
	fingerprint := ociFingerprint(&key.PublicKey)
	var matched []OCIProfile
	for _, p := range profiles {
		if p.Fingerprint == fingerprint {
			matched = AppendOCIProfile(matched, p)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	return NewOCICredential(key, matched)
}

// parseOCIKey returns the RSA key in a PKCS#1 or PKCS#8 PEM block. An
// encrypted, truncated, or non-RSA block does not parse and gives nil.
func parseOCIKey(text string) *rsa.PrivateKey {
	block, _ := pem.Decode([]byte(normalizePEM(text)))
	if block == nil {
		return nil
	}
	var parsed any
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	default:
		return nil
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if err != nil || !ok {
		return nil
	}
	if bits := key.N.BitLen(); bits < ociMinKeyBits || bits > ociMaxKeyBits {
		return nil
	}
	return key
}

// normalizePEM undoes the two text forms that pem.Decode does not read: line
// breaks written as the two characters backslash-n (environment variables,
// JSON), and lines indented inside a YAML block or a heredoc.
func normalizePEM(text string) string {
	text = strings.ReplaceAll(text, `\r\n`, "\n")
	text = strings.ReplaceAll(text, `\n`, "\n")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimLeft(line, " \t")
	}
	return strings.Join(lines, "\n")
}

// ociFingerprint is the fingerprint OCI shows for an API key: the MD5 of the
// public key in DER form, as colon-separated hex. It equals
// `openssl rsa -pubout -outform DER | openssl md5 -c`.
func ociFingerprint(pub *rsa.PublicKey) string {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	sum := md5.Sum(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = hex.EncodeToString([]byte{b})
	}
	return strings.Join(parts, ":")
}
