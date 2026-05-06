//go:build windows

package cli

import "errors"

// chmodOrSkip is a no-op stub on Windows; the mode-bit branch of
// describeDirtyButNotStashed cannot be exercised because NTFS does not
// track unix mode bits. Returning an error makes the parent test skip
// instead of trying to assert on a hint that will never appear.
func chmodOrSkip(_ string) error {
	return errors.New("chmod is a no-op on this filesystem")
}
