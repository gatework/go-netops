// Package batch runs read-only collection across many devices with bounded
// concurrency and publishes a summary that always matches what is on disk.
//
// Every started device result is kept, even after a sibling's report failure
// or cancellation. A cancellation or output failure that arrives after the
// summary was published republishes it once, through the same journal, so
// the returned summary and the persisted one never disagree. Collection is
// never replayed to repair publication.
package batch

import (
	"cmp"
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/gatework/go-net-connector/parallel"
	"github.com/gatework/go-netops/evidence"
)

// SchemaVersion is the version of the Summary document.
const SchemaVersion = 3

// ErrInvalid reports invalid batch options.
var ErrInvalid = errors.New("batch: invalid options")

// Device is one collection target.
type Device struct {
	Host     string `json:"host"`
	Name     string `json:"name,omitempty"`
	Model    string `json:"model,omitempty"`
	Platform string `json:"platform,omitempty"`
}

// DimensionSummary condenses one observed dimension.
type DimensionSummary struct {
	Status evidence.Status `json:"status"`
	Facts  int             `json:"facts"`
	Issues []string        `json:"issues"`
}

// Summarize builds a DimensionSummary with unique issue codes in order.
func Summarize(status evidence.Status, facts int, issues []evidence.Issue) DimensionSummary {
	codes := []string{}
	for _, i := range issues {
		if i.Code != "" && !slices.Contains(codes, i.Code) {
			codes = append(codes, i.Code)
		}
	}
	return DimensionSummary{Status: status, Facts: facts, Issues: codes}
}

// Collection is what a collector reports about one device besides its
// domain snapshot.
type Collection struct {
	Status     evidence.Status
	Dimensions map[evidence.Dimension]DimensionSummary
}

// DeviceResult is the persisted per-device outcome.
type DeviceResult struct {
	Device      Device                                  `json:"device"`
	Status      evidence.Status                         `json:"status"`
	Dimensions  map[evidence.Dimension]DimensionSummary `json:"dimensions"`
	Error       string                                  `json:"error,omitempty"`
	DurationMS  int64                                   `json:"duration_ms"`
	ReportSaved bool                                    `json:"report_saved"`
}

// PublicationFailure is safe metadata about a failed report write.
type PublicationFailure struct {
	Code      string `json:"code"`
	Committed bool   `json:"committed"`
}

// Summary is the batch outcome.
type Summary struct {
	SchemaVersion  int                     `json:"schema_version"`
	Requested      int                     `json:"requested"`
	Devices        []DeviceResult          `json:"devices"`
	Counts         map[evidence.Status]int `json:"counts"`
	Interrupted    bool                    `json:"interrupted"`
	ReportError    *PublicationFailure     `json:"report_error,omitempty"`
	ExecutionError string                  `json:"execution_error,omitempty"`
	OutputError    string                  `json:"output_error,omitempty"`
}

// Journal persists device results and the summary. S is the domain snapshot.
type Journal[S any] interface {
	// WriteDevice persists one device; snapshot is nil when collection failed.
	WriteDevice(ctx context.Context, result DeviceResult, snapshot *S) error
	// Finish publishes the summary; it may be called more than once.
	Finish(ctx context.Context, summary Summary) error
	// RecoveryFailed reports whether any checkpoint restore failed.
	RecoveryFailed() bool
}

// Events are optional, synchronous progress callbacks.
type Events struct {
	Started   func(Device)
	Completed func(DeviceResult)
	Finalized func(Summary)
	// Deliver hands the final summary to the caller's output. A failure is
	// recorded in the summary and republished.
	Deliver func(Summary) error
	// OutputError reports a progress-output failure observed so far.
	OutputError func() error
}

// Options configures Run.
type Options[S any] struct {
	Workers int
	Devices []Device
	Collect func(context.Context, Device) (S, Collection, error)
	Journal Journal[S]
	Events  Events
	Now     func() time.Time
}

// Result is the terminal outcome. Summary is the last confirmed publication,
// or nil when the last publication failed.
type Result struct {
	Summary                                                 *Summary
	Interrupted, Failed, FinalizationFailed, RecoveryFailed bool
}

type committed interface {
	error
	Committed() bool
}

func publicationFailure(err error) *PublicationFailure {
	f := &PublicationFailure{Code: "report_failed"}
	if c, ok := errors.AsType[committed](err); ok {
		f.Committed = c.Committed()
	}
	return f
}

// Run collects every device and publishes the summary.
func Run[S any](ctx context.Context, o Options[S]) (Result, error) {
	if o.Collect == nil || o.Journal == nil {
		return Result{Failed: true, Interrupted: ctx.Err() != nil}, ErrInvalid
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	r := &batchRun[S]{ctx: ctx, o: o, cancel: cancel, results: []DeviceResult{}}
	execErr := r.collect(batchCtx)
	r.summarize(execErr)
	return r.settle(execErr)
}

// batchRun is one execution of Run.
type batchRun[S any] struct {
	ctx    context.Context //nolint:containedctx // the caller's context, scoped to one Run call
	o      Options[S]
	cancel context.CancelFunc

	mu             sync.Mutex // guards results and firstReportErr while devices run
	results        []DeviceResult
	firstReportErr error

	summary   Summary
	out       Result
	finishErr error
}

// collect runs every device and returns the first execution error.
func (r *batchRun[S]) collect(batchCtx context.Context) error {
	tasks, execErr := parallel.Map(batchCtx, r.o.Workers, r.o.Devices, r.device)
	if execErr == nil {
		for _, t := range tasks {
			if t.Started && t.Err != nil {
				return t.Err
			}
		}
	}
	return execErr
}

// device collects one device and persists its result. Every started result
// is persisted, even after a sibling failure; a persistence failure cancels
// the batch.
func (r *batchRun[S]) device(taskCtx context.Context, d Device) (struct{}, error) {
	start := r.o.Now()
	if r.o.Events.Started != nil {
		r.o.Events.Started(d)
	}
	snapshot, collection, err := r.o.Collect(taskCtx, d)
	result := DeviceResult{Device: d, Status: evidence.StatusFailed, Dimensions: map[evidence.Dimension]DimensionSummary{}}
	var saved *S
	if err != nil {
		result.Error = "collection_failed"
	} else {
		saved = &snapshot
		result.Status = collection.Status
		maps.Copy(result.Dimensions, collection.Dimensions)
	}
	result.DurationMS = r.o.Now().Sub(start).Round(time.Millisecond).Milliseconds()
	r.mu.Lock()
	index := len(r.results)
	r.results = append(r.results, result)
	r.mu.Unlock()
	err = r.o.Journal.WriteDevice(context.WithoutCancel(taskCtx), result, saved)
	r.mu.Lock()
	if err == nil {
		r.results[index].ReportSaved = true
		result.ReportSaved = true
	} else if r.firstReportErr == nil {
		r.firstReportErr = err
	}
	r.mu.Unlock()
	if err != nil {
		r.cancel()
		return struct{}{}, err
	}
	if r.o.Events.Completed != nil {
		r.o.Events.Completed(result)
	}
	return struct{}{}, nil
}

// summarize builds the summary from the finished device results.
func (r *batchRun[S]) summarize(execErr error) {
	slices.SortFunc(r.results, func(a, b DeviceResult) int { return cmp.Compare(a.Device.Host, b.Device.Host) })
	r.summary = Summary{SchemaVersion: SchemaVersion, Requested: len(r.o.Devices), Devices: r.results, Counts: map[evidence.Status]int{}, Interrupted: r.ctx.Err() != nil}
	for _, d := range r.results {
		r.summary.Counts[d.Status]++
	}
	if r.outputErr() != nil {
		r.summary.OutputError = "output_failed"
	}
	if r.firstReportErr != nil {
		r.summary.ReportError = publicationFailure(r.firstReportErr)
	} else if execErr != nil && !r.summary.Interrupted {
		r.summary.ExecutionError = "execution_failed"
	}
}

func (r *batchRun[S]) outputErr() error {
	if r.o.Events.OutputError == nil {
		return nil
	}
	return r.o.Events.OutputError()
}

// publish hands the summary to the journal once, recording a previous
// publication failure in it first.
func (r *batchRun[S]) publish() {
	if r.finishErr != nil {
		r.summary.ReportError = publicationFailure(r.finishErr)
	}
	r.finishErr = r.o.Journal.Finish(context.WithoutCancel(r.ctx), r.summary)
	if r.finishErr != nil {
		r.out.Summary = nil
	} else {
		s := r.summary
		r.out.Summary = &s
	}
	r.out.FinalizationFailed = r.out.FinalizationFailed || r.finishErr != nil
	r.out.RecoveryFailed = r.out.RecoveryFailed || r.o.Journal.RecoveryFailed()
}

// finalize publishes the summary; cancellation is one-way, so if it arrived
// during Finish the summary is published once more as interrupted.
func (r *batchRun[S]) finalize() {
	r.publish()
	if r.ctx.Err() != nil && !r.summary.Interrupted {
		r.summary.Interrupted = true
		r.publish()
	}
	r.out.Interrupted = r.summary.Interrupted
}

// settle publishes the summary, republishes it after late cancellation or
// output failure, delivers it, and returns the terminal outcome.
func (r *batchRun[S]) settle(execErr error) (Result, error) {
	r.finalize()
	if !r.out.FinalizationFailed && r.o.Events.Finalized != nil {
		r.o.Events.Finalized(r.summary)
	}
	// Progress output may fail or observe a signal after publication.
	lateCancel := r.ctx.Err() != nil && !r.summary.Interrupted
	lateOutput := r.outputErr() != nil && r.summary.OutputError == ""
	if lateCancel || lateOutput {
		r.summary.Interrupted = r.summary.Interrupted || lateCancel
		if lateOutput {
			r.summary.OutputError = "output_failed"
		}
		r.finalize()
	}
	if r.out.FinalizationFailed {
		r.out.Failed = true
		return r.out, errors.Join(r.finishErr, r.firstReportErr, execErr)
	}
	if r.o.Events.Deliver != nil {
		if err := r.o.Events.Deliver(r.summary); err != nil {
			r.summary.Interrupted = r.summary.Interrupted || r.ctx.Err() != nil
			r.summary.OutputError = "output_failed"
			r.finalize()
			r.out.Failed = true
			return r.out, err
		}
	}
	if r.ctx.Err() != nil && !r.summary.Interrupted {
		r.summary.Interrupted = true
		r.finalize()
	}
	r.out.Failed = r.out.FinalizationFailed || r.out.Interrupted || r.firstReportErr != nil || execErr != nil
	return r.out, errors.Join(r.firstReportErr, execErr)
}
