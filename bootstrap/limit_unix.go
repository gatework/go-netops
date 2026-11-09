//go:build unix

package bootstrap

import "golang.org/x/sys/unix"

func getLimit() (uint64, uint64, error) {
	var l unix.Rlimit
	err := unix.Getrlimit(unix.RLIMIT_NOFILE, &l)
	return uint64(l.Cur), uint64(l.Max), err
}

func setLimit(cur, max uint64) error {
	l := unix.Rlimit{Cur: cur, Max: max}
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &l)
}
