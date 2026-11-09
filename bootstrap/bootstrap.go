// Package bootstrap holds process-level preparation shared by network
// operations CLIs: file-descriptor budgets and path resolution.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// MaxActiveDevices bounds concurrent device sessions per process.
const MaxActiveDevices = 500

var (
	// ErrInvalid reports an out-of-range request.
	ErrInvalid = errors.New("bootstrap: invalid request")
	// ErrInsufficientResources reports a descriptor limit that cannot be raised.
	ErrInsufficientResources = errors.New("bootstrap: insufficient file descriptors")
)

// The limit is process-wide, so read and raise form one critical section.
var limitMu sync.Mutex

// RequiredDescriptors is the descriptor budget for active device sessions:
// a fixed base plus one PTY, process and pipe set per device.
func RequiredDescriptors(active int) uint64 { return uint64(128 + 16*active) }

// PrepareFileLimit raises the soft RLIMIT_NOFILE to fit active sessions,
// never above the hard limit. It is a no-op where the limit is not managed.
func PrepareFileLimit(ctx context.Context, active int) error {
	return prepare(ctx, active, getLimit, setLimit)
}

func prepare(ctx context.Context, active int, get func() (cur, max uint64, err error), set func(cur, max uint64) error) error {
	if active < 0 || active > MaxActiveDevices {
		return ErrInvalid
	}
	if err := ctx.Err(); err != nil || active == 0 {
		return err
	}
	limitMu.Lock()
	defer limitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	required := RequiredDescriptors(active)
	failure := fmt.Errorf("%w: %d active devices require %d descriptors", ErrInsufficientResources, active, required)
	cur, hard, err := get()
	if err != nil {
		return failure
	}
	if cur >= required {
		return nil
	}
	if hard < required || set(required, hard) != nil {
		return failure
	}
	return nil
}

// ResolvePath expands "~" against home and makes p absolute against base.
func ResolvePath(p, base, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
		return filepath.Clean(p)
	}
	return filepath.Join(base, p)
}
