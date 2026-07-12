//go:build unix && !freebsd && !dragonfly

package cli

// rlimVal mirrors the platform type of syscall.Rlimit's Cur/Max fields —
// uint64 everywhere except the BSDs that declare them int64. The alias lets
// fdlimit_unix.go assign ladder values without per-platform copies of the
// logic (a plain uint64 literal fails to compile on FreeBSD/DragonFly).
type rlimVal = uint64
