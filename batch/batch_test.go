package batch_test

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"

	"github.com/gatework/go-netops/batch"
	"github.com/gatework/go-netops/evidence"
)

type snapshot struct{ host string }

type journal struct {
	mu        sync.Mutex
	devices   []batch.DeviceResult
	summaries []batch.Summary
	failHost  string
	finishErr []error // consumed per Finish call
	recovery  bool
}

func (j *journal) WriteDevice(_ context.Context, r batch.DeviceResult, s *snapshot) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.devices = append(j.devices, r)
	if r.Device.Host == j.failHost {
		return errors.New("disk full")
	}
	return nil
}
func (j *journal) Finish(_ context.Context, s batch.Summary) error {
	j.summaries = append(j.summaries, s)
	if len(j.finishErr) > 0 {
		err := j.finishErr[0]
		j.finishErr = j.finishErr[1:]
		return err
	}
	return nil
}
func (j *journal) RecoveryFailed() bool { return j.recovery }

func devices(hosts ...string) []batch.Device {
	var out []batch.Device
	for _, h := range hosts {
		out = append(out, batch.Device{Host: h, Name: "sw-" + h})
	}
	return out
}

func collect(_ context.Context, d batch.Device) (snapshot, batch.Collection, error) {
	if d.Host == "10.0.0.9" {
		return snapshot{}, batch.Collection{}, errors.New("unreachable")
	}
	return snapshot{d.Host}, batch.Collection{Status: evidence.StatusComplete, Dimensions: map[evidence.Dimension]batch.DimensionSummary{
		"nve_peers": batch.Summarize(evidence.StatusComplete, 2, nil),
	}}, nil
}

func TestRunSummarizesSortedResults(t *testing.T) {
	j := &journal{}
	var delivered *batch.Summary
	res, err := batch.Run(t.Context(), batch.Options[snapshot]{
		Workers: 2, Devices: devices("10.0.0.3", "10.0.0.9", "10.0.0.1"), Collect: collect, Journal: j,
		Events: batch.Events{Deliver: func(s batch.Summary) error { delivered = &s; return nil }},
	})
	if err != nil || res.Failed || res.Summary == nil || delivered == nil {
		t.Fatalf("%+v %v", res, err)
	}
	s := res.Summary
	hosts := []string{}
	for _, d := range s.Devices {
		hosts = append(hosts, d.Device.Host)
	}
	if !slices.Equal(hosts, []string{"10.0.0.1", "10.0.0.3", "10.0.0.9"}) || s.Counts[evidence.StatusComplete] != 2 || s.Counts[evidence.StatusFailed] != 1 {
		t.Fatalf("%+v", s)
	}
	if s.Devices[2].Error != "collection_failed" || !s.Devices[0].ReportSaved || s.Requested != 3 || len(j.summaries) != 1 {
		t.Fatalf("%+v", s)
	}
}

func TestReportFailureKeepsStartedResults(t *testing.T) {
	j := &journal{failHost: "10.0.0.1"}
	res, err := batch.Run(t.Context(), batch.Options[snapshot]{Workers: 1, Devices: devices("10.0.0.1", "10.0.0.2"), Collect: collect, Journal: j})
	if err == nil || !res.Failed || res.Summary.ReportError == nil || res.Summary.ReportError.Code != "report_failed" {
		t.Fatalf("%+v", res)
	}
	if res.Summary.Devices[0].ReportSaved {
		t.Fatal("failed device marked saved")
	}
}

func TestFinishFailureClearsSummary(t *testing.T) {
	j := &journal{finishErr: []error{errors.New("rename")}}
	res, err := batch.Run(t.Context(), batch.Options[snapshot]{Devices: devices("10.0.0.1"), Collect: collect, Journal: j})
	if err == nil || !res.FinalizationFailed || res.Summary != nil {
		t.Fatalf("%+v", res)
	}
}

func TestLateOutputFailureIsRepublished(t *testing.T) {
	j := &journal{}
	var outputErr error
	_, _ = batch.Run(t.Context(), batch.Options[snapshot]{Devices: devices("10.0.0.1"), Collect: collect, Journal: j,
		Events: batch.Events{
			Finalized:   func(batch.Summary) { outputErr = errors.New("EPIPE") },
			OutputError: func() error { return outputErr },
		}})
	if len(j.summaries) != 2 || j.summaries[0].OutputError != "" || j.summaries[1].OutputError != "output_failed" {
		t.Fatalf("%+v", j.summaries)
	}
}

func TestDeliverFailureIsRecorded(t *testing.T) {
	j := &journal{}
	res, err := batch.Run(t.Context(), batch.Options[snapshot]{Devices: devices("10.0.0.1"), Collect: collect, Journal: j,
		Events: batch.Events{Deliver: func(batch.Summary) error { return errors.New("closed stdout") }}})
	if err == nil || !res.Failed || j.summaries[len(j.summaries)-1].OutputError != "output_failed" {
		t.Fatalf("%+v", res)
	}
}

func TestCancellationIsPublishedAsInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	j := &journal{}
	res, _ := batch.Run(ctx, batch.Options[snapshot]{Devices: devices("10.0.0.1"), Collect: collect, Journal: j})
	if !res.Interrupted || !res.Failed || !j.summaries[len(j.summaries)-1].Interrupted {
		t.Fatalf("%+v", res)
	}
	if _, err := batch.Run(t.Context(), batch.Options[snapshot]{}); !errors.Is(err, batch.ErrInvalid) {
		t.Fatal("empty options accepted")
	}
}

func TestSummarizeDeduplicatesIssues(t *testing.T) {
	s := batch.Summarize(evidence.StatusPartial, 3, []evidence.Issue{{Code: "unparsed_row", Line: 1}, {Code: "unparsed_row", Line: 2}, {Code: "truncated"}})
	if !slices.Equal(s.Issues, []string{"unparsed_row", "truncated"}) {
		t.Fatal(s.Issues)
	}
}
