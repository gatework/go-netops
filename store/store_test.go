package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gatework/go-net-connector/storage"
	"github.com/gatework/go-netops/apply"
	"github.com/gatework/go-netops/store"
)

func newStore(t *testing.T, write store.WriteFunc) *store.Store {
	t.Helper()
	s, err := store.New(filepath.Join(t.TempDir(), "reports"), store.Options{Now: func() time.Time { return time.Unix(0, 0) }, Write: write})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPublishIsPrivate(t *testing.T) {
	s := newStore(t, nil)
	if err := s.PublishJSON(t.Context(), "devices/a.json", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(s.Directory(), "devices/a.json"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v err %v", info.Mode(), err)
	}
	dir, _ := os.Stat(filepath.Join(s.Directory(), "devices"))
	if dir.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode %v", dir.Mode())
	}
	for _, bad := range []string{"", "/abs", "../x", "a/../../x", "a\nb"} {
		if err := s.Publish(t.Context(), bad, nil); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}

func TestPublishErrorReportsVisibility(t *testing.T) {
	s := newStore(t, func(ctx context.Context, path string, body []byte) (storage.Receipt, error) {
		return storage.Receipt{Path: path, State: storage.StateCommitted, Phase: "sync_directory"}, context.Canceled
	})
	err := s.Publish(t.Context(), "x.json", []byte("{}"))
	pe, ok := errors.AsType[*store.PublishError](err)
	if !ok || !pe.Committed() || pe.Durable || !errors.Is(err, context.Canceled) {
		t.Fatalf("%#v", err)
	}
}

func verified() apply.State {
	return apply.State{Status: apply.StatusVerified, ObservedConfigSHA256: "x", Commands: []string{"a"}, Steps: []apply.Step{{Index: 0, Status: "acknowledged"}}}
}

func readState(t *testing.T, s *store.Store, name string) apply.State {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(s.Directory(), name))
	if err != nil {
		t.Fatal(err)
	}
	var st apply.State
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func TestJournalFinalizeKeepsCheckpoint(t *testing.T) {
	s := newStore(t, nil)
	j := store.NewJournal(s)
	if err := j.FinalizeExecution(t.Context(), verified()); err != nil {
		t.Fatal(err)
	}
	if readState(t, s, "execution.json").Status != apply.StatusVerified || readState(t, s, "execution-checkpoint.json").Status != apply.StatusUncertain {
		t.Fatal("final or checkpoint state wrong")
	}
	if err := j.FinalizeExecution(t.Context(), apply.State{Status: apply.StatusExecuting}); err == nil {
		t.Fatal("finalized an unverified state")
	}
}

func TestJournalRestoresCheckpointWhenFinalVisibleButFailed(t *testing.T) {
	calls := 0
	s := newStore(t, func(ctx context.Context, path string, body []byte) (storage.Receipt, error) {
		calls++
		r, err := storage.Write(ctx, path, body)
		if calls == 2 { // the final execution.json replacement
			return storage.Receipt{Path: path, State: storage.StateCommitted, Phase: "sync_directory"}, errors.New("fsync")
		}
		return r, err
	})
	if err := store.NewJournal(s).FinalizeExecution(t.Context(), verified()); err == nil {
		t.Fatal("expected failure")
	}
	if st := readState(t, s, "execution.json"); st.Status != apply.StatusUncertain || st.Error != apply.ErrorFinalizationUnconfirmed {
		t.Fatalf("unconfirmed success left on disk: %+v", st)
	}
}

func TestBasename(t *testing.T) {
	if got := store.Basename("leaf 1/a", "fe80::1"); got != "leaf_1_a-fe80__1" {
		t.Fatal(got)
	}
}
