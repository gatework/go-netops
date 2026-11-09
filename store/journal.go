package store

import (
	"context"
	"errors"

	"github.com/gatework/go-netops/apply"
)

const (
	executionFile  = "execution.json"
	checkpointFile = "execution-checkpoint.json"
)

// Journal records apply states in the store and satisfies apply.Journal.
type Journal struct{ store *Store }

// NewJournal returns an execution journal backed by s.
func NewJournal(s *Store) *Journal { return &Journal{store: s} }

// WriteExecution publishes the current state.
func (j *Journal) WriteExecution(ctx context.Context, state apply.State) error {
	return j.store.PublishJSON(ctx, executionFile, state)
}

// FinalizeExecution first writes an independent "uncertain" checkpoint, then
// replaces execution.json with the verified state. If that replacement fails
// or is interrupted after possibly becoming visible, the checkpoint content
// is restored so a crash never leaves an unconfirmed success on disk.
func (j *Journal) FinalizeExecution(ctx context.Context, final apply.State) error {
	if final.Status != apply.StatusVerified || final.ObservedConfigSHA256 == "" {
		return &PublishError{Relative: executionFile, Phase: "checkpoint"}
	}
	checkpoint := final
	checkpoint.Status = apply.StatusUncertain
	checkpoint.Error = apply.ErrorFinalizationUnconfirmed
	if err := j.store.PublishJSON(ctx, checkpointFile, checkpoint); err != nil {
		return err
	}
	err := j.WriteExecution(ctx, final)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		return nil
	}
	pe, isPublish := errors.AsType[*PublishError](err)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || isPublish && pe.Visible {
		if restoreErr := j.store.PublishJSON(context.WithoutCancel(ctx), executionFile, checkpoint); restoreErr != nil {
			j.store.mu.Lock()
			j.store.degraded = true
			j.store.mu.Unlock()
		}
	}
	return err
}
