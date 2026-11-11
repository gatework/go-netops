package apply_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/gatework/go-netops/apply"
)

type plan struct {
	commands    []string
	beforeErr   error
	afterErr    error
	fingerprint string
}

func (p plan) Commands() []string       { return slices.Clone(p.commands) }
func (p plan) CheckBefore(string) error { return p.beforeErr }
func (p plan) CheckAfter(_, _ string) (string, error) {
	return p.fingerprint, p.afterErr
}

type session struct {
	written  []string
	writeErr error
	failAt   int
	readErr  error
	cancel   context.CancelFunc
}

func (s *session) ReadConfiguration(context.Context) (string, error) { return "cfg", s.readErr }
func (s *session) Write(_ context.Context, c string) error {
	if s.writeErr != nil && len(s.written) == s.failAt {
		return s.writeErr
	}
	s.written = append(s.written, c)
	if s.cancel != nil {
		s.cancel()
	}
	return nil
}

type journal struct {
	states   []apply.State
	failOn   int // 1-based WriteExecution call to fail; 0 never
	visible  bool
	finalErr error
	final    *apply.State
}

type committedError struct{}

func (committedError) Error() string   { return "published but not durable" }
func (committedError) Committed() bool { return true }

func (j *journal) WriteExecution(_ context.Context, s apply.State) error {
	j.states = append(j.states, s)
	if j.failOn == len(j.states) {
		if j.visible {
			return committedError{}
		}
		return errors.New("disk full")
	}
	return nil
}
func (j *journal) FinalizeExecution(_ context.Context, s apply.State) error {
	j.final = &s
	return j.finalErr
}

func opener(s *session, cleanup error) apply.Opener[string] {
	return func(ctx context.Context, run func(context.Context, apply.Session[string]) error) (error, error) {
		return run(ctx, s), cleanup
	}
}

func request(p plan, s *session, j *journal, cleanup error) apply.Request[string] {
	return apply.Request[string]{
		Plan:     p,
		Identity: apply.State{Device: apply.Device{Host: "10.0.0.1", Platform: "cisco_nxos"}, PlanSHA256: "p", ExpectedConfigSHA256: "e"},
		Open:     opener(s, cleanup),
		Journal:  j,
		Classify: func(err error) []string { return []string{"classified"} },
	}
}

var twoCommands = plan{commands: []string{"a", "b"}, fingerprint: "after"}

func TestRunVerifies(t *testing.T) {
	s, j := &session{}, &journal{}
	res, err := apply.Run(t.Context(), request(twoCommands, s, j, nil))
	if err != nil {
		t.Fatal(err)
	}
	if res.State.Status != apply.StatusVerified || res.State.ObservedConfigSHA256 != "after" || len(res.State.Steps) != 2 {
		t.Fatalf("state %+v", res.State)
	}
	if !slices.Equal(s.written, []string{"a", "b"}) || j.final == nil {
		t.Fatalf("written %v final %v", s.written, j.final)
	}
	// confirmed, intent a, ack a, intent b, ack b
	statuses := []apply.Status{}
	for _, st := range j.states {
		statuses = append(statuses, st.Status)
	}
	if !slices.Equal(statuses, []apply.Status{"confirmed", "executing", "executing", "executing", "executing"}) || j.states[1].PendingCommand == nil || *j.states[1].PendingCommand != 0 || j.states[2].PendingCommand != nil {
		t.Fatalf("journal sequence %+v", j.states)
	}
}

func TestRunFailsBeforeSendingIsFailed(t *testing.T) {
	s, j := &session{}, &journal{}
	res, err := apply.Run(t.Context(), request(plan{commands: []string{"a"}, beforeErr: errors.New("drift")}, s, j, nil))
	if err == nil || res.State.Status != apply.StatusFailed || len(s.written) != 0 || res.State.FailedStage != apply.StageReadback {
		t.Fatalf("%+v %v", res.State, err)
	}
}

func TestRunWriteFailureAfterIntentIsUncertain(t *testing.T) {
	s, j := &session{writeErr: errors.New("timeout"), failAt: 1}, &journal{}
	res, err := apply.Run(t.Context(), request(twoCommands, s, j, nil))
	if err == nil || res.State.Status != apply.StatusUncertain || res.State.PendingCommand == nil || *res.State.PendingCommand != 1 {
		t.Fatalf("%+v", res.State)
	}
	if len(res.State.Diagnostics) == 0 || res.State.Diagnostics[0].Code != "classified" || res.State.FailedStage != apply.StageExecute {
		t.Fatalf("diagnostics %+v", res.State)
	}
	if len(s.written) != 1 {
		t.Fatalf("replayed or skipped: %v", s.written)
	}
}

func TestRunIntentFailureIsFailedUnlessVisible(t *testing.T) {
	for _, visible := range []bool{false, true} {
		s, j := &session{}, &journal{failOn: 2, visible: visible}
		res, err := apply.Run(t.Context(), request(twoCommands, s, j, nil))
		want := apply.StatusFailed
		if visible {
			want = apply.StatusUncertain
		}
		if err == nil || res.State.Status != want || len(s.written) != 0 {
			t.Fatalf("visible=%v: %+v", visible, res.State)
		}
	}
}

func TestRunCleanupFailureInvalidatesSuccess(t *testing.T) {
	s, j := &session{}, &journal{}
	res, err := apply.Run(t.Context(), request(twoCommands, s, j, errors.New("close")))
	if err == nil || res.State.Status != apply.StatusUncertain || j.final != nil {
		t.Fatalf("%+v", res.State)
	}
	if !slices.ContainsFunc(res.State.Diagnostics, func(d apply.Diagnostic) bool { return d.Stage == apply.StageClose }) {
		t.Fatalf("cleanup not diagnosed: %+v", res.State.Diagnostics)
	}
}

func TestRunFinalizationFailureIsUnconfirmed(t *testing.T) {
	s, j := &session{}, &journal{finalErr: errors.New("rename")}
	res, err := apply.Run(t.Context(), request(twoCommands, s, j, nil))
	if err == nil || !res.ReportUnconfirmed || res.State.Status != apply.StatusUncertain || res.State.Error != apply.ErrorFinalizationUnconfirmed {
		t.Fatalf("%+v", res)
	}
}

func TestRunCancellationStopsBeforeNextCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	s, j := &session{cancel: cancel}, &journal{}
	res, err := apply.Run(ctx, request(twoCommands, s, j, nil))
	if !errors.Is(err, context.Canceled) || !res.Interrupted || res.State.Error != apply.ErrorInterrupted || len(s.written) != 1 || res.State.Status != apply.StatusUncertain {
		t.Fatalf("%+v written=%v", res, s.written)
	}
}

func TestRunRejectsReplayingOpener(t *testing.T) {
	s, j := &session{}, &journal{}
	req := request(twoCommands, s, j, nil)
	req.Open = func(ctx context.Context, run func(context.Context, apply.Session[string]) error) (error, error) {
		_ = run(ctx, s)
		return run(ctx, s), nil
	}
	res, err := apply.Run(t.Context(), req)
	if !errors.Is(err, apply.ErrProtocol) || len(s.written) != 2 || res.State.Status != apply.StatusUncertain {
		t.Fatalf("replay admitted: %+v written=%v", res.State, s.written)
	}
}

func TestRunRejectsOpenerThatNeverRuns(t *testing.T) {
	s, j := &session{}, &journal{}
	req := request(twoCommands, s, j, nil)
	req.Open = func(context.Context, func(context.Context, apply.Session[string]) error) (error, error) {
		return nil, nil
	}
	if res, err := apply.Run(t.Context(), req); !errors.Is(err, apply.ErrProtocol) || res.State.Status != apply.StatusFailed {
		t.Fatalf("%+v %v", res.State, err)
	}
	if _, err := apply.Run(t.Context(), apply.Request[string]{}); !errors.Is(err, apply.ErrProtocol) {
		t.Fatal("empty request accepted")
	}
}
