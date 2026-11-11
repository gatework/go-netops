// Package store publishes private, durable report artifacts.
//
// Every artifact lives in one batch directory (mode 0700) and is written with
// go-net-connector's atomic, fsynced storage.Write (files 0600). A failed
// publication reports whether the new content became visible anyway, which
// the apply state machine needs to decide between "failed" and "uncertain".
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gatework/go-net-connector/storage"
)

// WriteFunc writes one file atomically. storage.Write is the default.
type WriteFunc func(ctx context.Context, path string, body []byte) (storage.Receipt, error)

// PublishError carries safe publication metadata and never the cause text.
type PublishError struct {
	Relative  string `json:"relative"`
	Phase     string `json:"phase"`
	Visible   bool   `json:"committed"`
	Durable   bool   `json:"durable"`
	cancelErr error
}

func (e *PublishError) Error() string {
	return fmt.Sprintf("store: %s at %s (committed=%t durable=%t)", e.Relative, e.Phase, e.Visible, e.Durable)
}

// GoString formats the same metadata as Error.
func (e *PublishError) GoString() string { return e.Error() }

// Committed reports whether the new content became visible despite the error.
func (e *PublishError) Committed() bool { return e.Visible }

// Is preserves cancellation identity without retaining private causes.
func (e *PublishError) Is(target error) bool {
	return e.cancelErr != nil && errors.Is(e.cancelErr, target)
}

// Options configures a Store.
type Options struct {
	Now   func() time.Time
	Write WriteFunc
}

// Store is one batch directory.
type Store struct {
	directory string
	write     WriteFunc
	mu        sync.Mutex
	degraded  bool
}

// New creates a fresh batch directory under root.
func New(root string, o Options) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("store: root required")
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	directory, err := storage.CreateBatchDirectory(root, now())
	if err != nil {
		return nil, errors.New("store: cannot create batch directory")
	}
	return Open(directory, o)
}

// Open uses an existing private directory as the batch.
func Open(directory string, o Options) (*Store, error) {
	stat, err := os.Lstat(directory)
	if err != nil || !stat.IsDir() || stat.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("store: batch must be a real private directory (0700)")
	}
	write := o.Write
	if write == nil {
		write = storage.Write
	}
	return &Store{directory: directory, write: write}, nil
}

// Directory returns the batch directory.
func (s *Store) Directory() string { return s.directory }

// RecoveryFailed reports whether a checkpoint restore ever failed.
func (s *Store) RecoveryFailed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.degraded
}

// Publish writes body at a path relative to the batch. Intermediate
// directories are created private and must not be symlinks.
func (s *Store) Publish(ctx context.Context, relative string, body []byte) error {
	if !safeRelative(relative) {
		return &PublishError{Relative: "[invalid]", Phase: "path"}
	}
	parent := s.directory
	for part := range strings.SplitSeq(filepath.Dir(relative), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		parent = filepath.Join(parent, part)
		if err := os.Mkdir(parent, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return &PublishError{Relative: relative, Phase: "directory"}
		}
		if st, err := os.Lstat(parent); err != nil || !st.IsDir() || st.Mode().Perm()&0o077 != 0 {
			return &PublishError{Relative: relative, Phase: "directory"}
		}
	}
	receipt, err := s.write(ctx, filepath.Join(s.directory, relative), body)
	if err == nil && !receipt.Durable() {
		err = errors.New("durability unconfirmed")
	}
	if err == nil {
		return nil
	}
	failure := &PublishError{Relative: relative, Phase: receipt.Phase, Visible: receipt.Committed(), Durable: receipt.Durable()}
	switch {
	case errors.Is(err, context.Canceled):
		failure.cancelErr = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		failure.cancelErr = context.DeadlineExceeded
	}
	return failure
}

// PublishJSON writes v as indented JSON with a trailing newline.
func (s *Store) PublishJSON(ctx context.Context, relative string, v any) error {
	body, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &PublishError{Relative: relative, Phase: "encode"}
	}
	return s.Publish(ctx, relative, append(body, '\n'))
}

func safeRelative(path string) bool {
	return path != "" && !filepath.IsAbs(path) && filepath.Clean(path) == path &&
		path != ".." && !strings.HasPrefix(path, ".."+string(filepath.Separator)) &&
		!strings.ContainsAny(path, "\x00\r\n")
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_.-]`)

// Basename returns a filesystem-safe artifact stem for a device.
func Basename(name, host string) string {
	name = unsafeName.ReplaceAllString(name, "_")
	if len(name) > 100 {
		name = name[:100]
	}
	return name + "-" + strings.ReplaceAll(host, ":", "_")
}
