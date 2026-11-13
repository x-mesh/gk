// Package codec decodes base64, hex, percent, and unicode escapes in place so
// the rules can run on the decoded text, and maps decoded matches back to
// their location in the original text.
//
// It is copied from detect/codec in gitleaks v8.30.1 (MIT License, see
// ../LICENSE-gitleaks) so gk reports the decoded secrets gitleaks reports.
// Changes: the standard regexp package replaces the gitleaks regexp wrapper,
// debug logging is removed, the test uses the standard testing package, and
// EncodedSegment.DecodedRange exposes where a segment sits in its pass.
package codec
