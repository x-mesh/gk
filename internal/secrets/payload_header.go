package secrets

import (
	"fmt"
	"regexp"
)

// PayloadFileHeader formats the file boundary marker that the secret-scan
// pipeline emits between files in its aggregated blob. The marker uses
// ">>> gk-file <path> <<<" so it cannot collide with markdown H3 headers
// (`### foo`) — an earlier `### <path>` convention caused README section
// titles like "### 첫 호출" to be mistaken for filenames in finding output.
//
// PayloadFileHeaderRE is the matching parser. Both must stay in sync;
// callers should never hand-roll either.
func PayloadFileHeader(path string) string {
	return fmt.Sprintf(">>> gk-file %s <<<", path)
}

// PayloadFileHeaderRE matches a single header line. Anchored on both
// ends so a stray ">>> gk-file foo <<<" appearing inside real content
// (e.g. quoted in a doc) cannot trigger a false boundary.
var PayloadFileHeaderRE = regexp.MustCompile(`^>>> gk-file (.+) <<<$`)
