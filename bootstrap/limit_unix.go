//go:build unix

package bootstrap

import "golang.org/x/sys/unix"

// rlimitValue is the type of unix.Rlimit fields: uint64 on most systems,
// int64 on FreeBSD.
type rlimitValue interface{ ~int64 | ~uint64 }

func fromRlimit[T rlimitValue](v T) uint64 { return uint64(v) }

func toRlimit[T rlimitValue](dst *T, v uint64) { *dst = T(v) }

func getLimit() (cur, hard uint64, err error) {
	var l unix.Rlimit
	err = unix.Getrlimit(unix.RLIMIT_NOFILE, &l)
	return fromRlimit(l.Cur), fromRlimit(l.Max), err
}

func setLimit(cur, hard uint64) error {
	var l unix.Rlimit
	toRlimit(&l.Cur, cur)
	toRlimit(&l.Max, hard)
	return unix.Setrlimit(unix.RLIMIT_NOFILE, &l)
}
