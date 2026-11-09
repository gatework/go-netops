package bootstrap

import (
	"context"
	"errors"
	"testing"
)

func TestPrepareRaisesSoftLimitWithinHard(t *testing.T) {
	var set uint64
	get := func() (uint64, uint64, error) { return 256, 10000, nil }
	put := func(cur, _ uint64) error { set = cur; return nil }
	if err := prepare(t.Context(), 10, get, put); err != nil || set != 288 {
		t.Fatalf("set=%d err=%v", set, err)
	}
	set = 0
	if err := prepare(t.Context(), 5, get, put); err != nil || set != 0 {
		t.Fatal("raised a sufficient limit")
	}
	low := func() (uint64, uint64, error) { return 256, 300, nil }
	if err := prepare(t.Context(), 100, low, put); !errors.Is(err, ErrInsufficientResources) {
		t.Fatalf("exceeded hard limit: %v", err)
	}
	if err := prepare(t.Context(), MaxActiveDevices+1, get, put); !errors.Is(err, ErrInvalid) {
		t.Fatal("accepted too many devices")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := prepare(ctx, 1, get, put); !errors.Is(err, context.Canceled) {
		t.Fatal("ignored cancellation")
	}
	if err := PrepareFileLimit(t.Context(), 1); err != nil {
		t.Fatalf("real limit: %v", err)
	}
}

func TestResolvePath(t *testing.T) {
	for _, c := range [][4]string{
		{"~", "/base", "/home/u", "/home/u"},
		{"~/r", "/base", "/home/u", "/home/u/r"},
		{"/a/../b", "/base", "/home/u", "/b"},
		{"rel", "/base", "/home/u", "/base/rel"},
	} {
		if got := ResolvePath(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("ResolvePath(%q) = %q", c[0], got)
		}
	}
}
