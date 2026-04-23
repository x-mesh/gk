package aicommit

import (
	"bytes"
	"os"
)

// DetectBinary reports whether the file at path is likely binary.
//
// The check reads up to 8 KiB and reports true when:
//   - the sample contains a NUL byte, or
//   - more than 1 in 32 bytes fall outside the printable ASCII + common
//     whitespace range (classic heuristic, same as `git diff`'s default).
//
// Returns false (and no error) when the file is missing — callers that
// care distinguish via os.Stat. IO errors surface up; they usually mean
// permission trouble worth reporting rather than papering over.
func DetectBinary(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer func() { _ = f.Close() }()

	buf := make([]byte, 8192)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return false, nil
	}
	sample := buf[:n]
	if bytes.IndexByte(sample, 0) >= 0 {
		return true, nil
	}
	nonText := 0
	for _, b := range sample {
		if b == '\n' || b == '\r' || b == '\t' {
			continue
		}
		if b < 0x20 || b == 0x7f {
			nonText++
		}
	}
	return nonText*32 > n, nil
}
