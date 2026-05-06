package update

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// AtomicReplace swaps `target` with `staged` and stashes the previous binary
// at `target + ".bak"` for trivial rollback. Both paths must be on the same
// filesystem — DownloadVerified arranges this by writing the staged file
// next to the target.
//
// On Linux/macOS, replacing a binary that is currently executing is safe:
// the kernel pins the old inode for the running process, while new exec
// calls find the new file via the path lookup. So the running `gk update`
// continues to operate from the old binary, and the very next `gk` call
// uses the upgraded one.
func AtomicReplace(staged, target string) error {
	if _, err := os.Stat(staged); err != nil {
		return fmt.Errorf("staged binary missing: %w", err)
	}

	bak := target + ".bak"
	// Remove a stale .bak from a previous interrupted run; otherwise Rename
	// can fail on filesystems that refuse to clobber existing files.
	_ = os.Remove(bak)

	// Preserve a copy of the current binary before clobbering it.
	if _, err := os.Stat(target); err == nil {
		if err := copyFile(target, bak); err != nil {
			return fmt.Errorf("backup current binary: %w", err)
		}
	}

	if err := os.Rename(staged, target); err != nil {
		return fmt.Errorf("install new binary at %s: %w", target, err)
	}
	if err := os.Chmod(target, 0o755); err != nil {
		return fmt.Errorf("chmod %s: %w", target, err)
	}
	return nil
}

// AtomicReplaceWithSudo wraps AtomicReplace with a privilege-escalation
// fallback for the common /usr/local/bin install. When the target directory
// is not writable by the current user, we shell out to:
//
//	sudo install -m 0755 <staged> <target>
//
// stdin/stdout/stderr are passed through so the user can answer the sudo
// password prompt directly. If sudo is missing we surface a clear error
// telling the user to rerun with privileges; we never silently fail.
//
// Backup behaviour is intentionally skipped under sudo — copying the prior
// binary as root introduces ownership questions (whose .bak is it?) that
// outweigh the rollback convenience for what is meant to be a rare path.
func AtomicReplaceWithSudo(staged, target string) error {
	if writable(filepath.Dir(target)) {
		return AtomicReplace(staged, target)
	}
	if _, err := exec.LookPath("sudo"); err != nil {
		return fmt.Errorf(
			"%s is not writable and sudo is unavailable; rerun with privileges or move %s to a user-writable location",
			filepath.Dir(target), target,
		)
	}
	cmd := exec.Command("sudo", "install", "-m", "0755", staged, target) //nolint:gosec // user-driven self-update
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("sudo install failed: %w", err)
	}
	// install(1) handled the move; clean up the staged file.
	_ = os.Remove(staged)
	return nil
}

// writable reports whether the current user can create files in `dir`. We
// check by trying to create and immediately remove a probe file rather than
// trusting Stat's permission bits, since ACLs and group membership make the
// bits-only approach unreliable across distros.
func writable(dir string) bool {
	probe, err := os.CreateTemp(dir, ".gk-update-probe-*")
	if err != nil {
		return false
	}
	name := probe.Name()
	probe.Close()
	_ = os.Remove(name)
	return true
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// ErrSameFilesystem signals an attempt to atomically rename across
// filesystems, which the OS rejects. Surfaced separately so callers can
// distinguish from other I/O errors and explain what happened.
var ErrSameFilesystem = errors.New("staged file and target must live on the same filesystem")
