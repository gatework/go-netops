package store

import (
	"context"

	"github.com/gatework/go-netops/batch"
)

// BatchJournal persists a collection batch: one devices/<basename>.json per
// device and summary.json. It satisfies batch.Journal[S].
type BatchJournal[S any] struct{ store *Store }

// NewBatchJournal returns a batch journal backed by s.
func NewBatchJournal[S any](s *Store) *BatchJournal[S] { return &BatchJournal[S]{store: s} }

type deviceDocument[S any] struct {
	SchemaVersion int                `json:"schema_version"`
	Result        batch.DeviceResult `json:"result"`
	Snapshot      *S                 `json:"snapshot"`
}

// WriteDevice publishes one device document.
func (j *BatchJournal[S]) WriteDevice(ctx context.Context, result batch.DeviceResult, snapshot *S) error {
	result.ReportSaved = false // the document cannot attest to its own durability
	return j.store.PublishJSON(ctx, "devices/"+Basename(result.Device.Name, result.Device.Host)+".json",
		deviceDocument[S]{SchemaVersion: batch.SchemaVersion, Result: result, Snapshot: snapshot})
}

// Finish publishes summary.json.
func (j *BatchJournal[S]) Finish(ctx context.Context, summary batch.Summary) error {
	return j.store.PublishJSON(ctx, "summary.json", summary)
}

// RecoveryFailed reports whether any checkpoint restore failed.
func (j *BatchJournal[S]) RecoveryFailed() bool { return j.store.RecoveryFailed() }
