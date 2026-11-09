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
	now := o.Now
	if now == nil {
		now = time.Now
	}
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var mu sync.Mutex
	results := []DeviceResult{}
	var firstReportErr error
	tasks, execErr := parallel.Map(batchCtx, o.Workers, o.Devices, func(taskCtx context.Context, d Device) (struct{}, error) {
		start := now()
		if o.Events.Started != nil {
			o.Events.Started(d)
		}
		snapshot, collection, err := o.Collect(taskCtx, d)
		result := DeviceResult{Device: d, Status: evidence.StatusFailed, Dimensions: map[evidence.Dimension]DimensionSummary{}}
		var saved *S
		if err != nil {
			result.Error = "collection_failed"
		} else {
			saved = &snapshot
			result.Status = collection.Status
			for name, s := range collection.Dimensions {
				result.Dimensions[name] = s
			}
		}
		result.DurationMS = now().Sub(start).Round(time.Millisecond).Milliseconds()
		mu.Lock()
		index := len(results)
		results = append(results, result)
		mu.Unlock()
		// Persist every started result, even after a sibling failure.
		err = o.Journal.WriteDevice(context.WithoutCancel(taskCtx), result, saved)
		mu.Lock()
		if err == nil {
			results[index].ReportSaved = true
			result.ReportSaved = true
		} else if firstReportErr == nil {
			firstReportErr = err
		}
		mu.Unlock()
		if err != nil {
			cancel()
			return struct{}{}, err
		}
		if o.Events.Completed != nil {
			o.Events.Completed(result)
		}
		return struct{}{}, nil
	})
	if execErr == nil {
		for _, t := range tasks {
			if t.Started && t.Err != nil {
				execErr = t.Err
				break
			}
		}
	}

	slices.SortFunc(results, func(a, b DeviceResult) int { return cmp.Compare(a.Device.Host, b.Device.Host) })
	summary := Summary{SchemaVersion: SchemaVersion, Requested: len(o.Devices), Devices: results, Counts: map[evidence.Status]int{}, Interrupted: ctx.Err() != nil}
	for _, r := range results {
		summary.Counts[r.Status]++
	}
	outputErr := func() error {
		if o.Events.OutputError == nil {
			return nil
		}
		return o.Events.OutputError()
	}
	if outputErr() != nil {
		summary.OutputError = "output_failed"
	}
	if firstReportErr != nil {
		summary.ReportError = publicationFailure(firstReportErr)
	} else if execErr != nil && !summary.Interrupted {
		summary.ExecutionError = "execution_failed"
	}

	out := Result{}
	var finishErr error
	publish := func() {
		if finishErr != nil {
			summary.ReportError = publicationFailure(finishErr)
		}
		finishErr = o.Journal.Finish(context.WithoutCancel(ctx), summary)
		if finishErr != nil {
			out.Summary = nil
		} else {
			s := summary
			out.Summary = &s
		}
		out.FinalizationFailed = out.FinalizationFailed || finishErr != nil
		out.RecoveryFailed = out.RecoveryFailed || o.Journal.RecoveryFailed()
	}
	finalize := func() {
		publish()
		// Cancellation is one-way: if it arrived during Finish, publish once more.
		if ctx.Err() != nil && !summary.Interrupted {
			summary.Interrupted = true
			publish()
		}
		out.Interrupted = summary.Interrupted
	}
	finalize()
	if !out.FinalizationFailed && o.Events.Finalized != nil {
		o.Events.Finalized(summary)
	}
	// Progress output may fail or observe a signal after publication.
	lateCancel := ctx.Err() != nil && !summary.Interrupted
	lateOutput := outputErr() != nil && summary.OutputError == ""
	if lateCancel || lateOutput {
		summary.Interrupted = summary.Interrupted || lateCancel
		if lateOutput {
			summary.OutputError = "output_failed"
		}
		finalize()
	}
	if out.FinalizationFailed {
		out.Failed = true
		return out, errors.Join(finishErr, firstReportErr, execErr)
	}
	if o.Events.Deliver != nil {
		if err := o.Events.Deliver(summary); err != nil {
			summary.Interrupted = summary.Interrupted || ctx.Err() != nil
			summary.OutputError = "output_failed"
			finalize()
			out.Failed = true
			return out, err
		}
	}
	if ctx.Err() != nil && !summary.Interrupted {
		summary.Interrupted = true
		finalize()
	}
	out.Failed = out.FinalizationFailed || out.Interrupted || firstReportErr != nil || execErr != nil
	return out, errors.Join(firstReportErr, execErr)
}
