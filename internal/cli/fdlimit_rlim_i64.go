//go:build freebsd || dragonfly

package cli

// rlimVal mirrors the platform type of syscall.Rlimit's Cur/Max fields —
// int64 on FreeBSD/DragonFly. See fdlimit_rlim_u64.go.
type rlimVal = int64
