// Package apply executes one confirmed plan against one device.
//
// The state machine is the one go-vxlan and go-wireless shared:
//
//	confirmed → executing(pending_command=i) → acknowledged step i → … →
//	readback verified → configuration_verified
//
// Intent is journaled before each send and acknowledgement after it. Once an
// intent may have been committed, any later failure is "uncertain". Run never
// retries, reconnects, replays a command, rolls back or saves configuration.
package apply

import (
	"context"
	"errors"
	"slices"
	"sync"
)

// ErrProtocol reports misuse of Run's contract by the caller or the opener.
var ErrProtocol = errors.New("apply: protocol violation")

// Status is the execution state recorded in the journal.
type Status string

const (
	StatusConfirmed Status = "confirmed"
	StatusExecuting Status = "executing"
	StatusVerified  Status = "configuration_verified"
	StatusFailed    Status = "failed"
	// StatusUncertain: a command may have reached the device but the outcome
	// could not be confirmed. Never retry automatically.
	StatusUncertain Status = "uncertain"
)

// Error codes recorded in State.Error.
const (
	ErrorExecutionFailed         = "execution_failed"
	ErrorFinalizationUnconfirmed = "execution_finalization_unconfirmed"
	ErrorInterrupted             = "interrupted"
	ErrorCleanupFailed           = "session_close_failed"
)

// Stage is where a failure happened.
type Stage string

const (
	StageConnect  Stage = "connect"
	StageReadback Stage = "response"
	StageExecute  Stage = "execute"
	StageClose    Stage = "close"
	StageJournal  Stage = "journal"
)

// Device identifies the target in the journal.
type Device struct {
	Host     string `json:"host"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform"`
}

// Step is one acknowledged command.
type Step struct {
	Index  int    `json:"index"`
	Status string `json:"status"`
}

// Diagnostic is a safe, code-only failure record.
type Diagnostic struct {
	Code  string `json:"code"`
	Stage Stage  `json:"stage"`
}

// State is the journaled execution record.
type State struct {
	Status               Status       `json:"status"`
	Device               Device       `json:"device"`
	PlanSHA256           string       `json:"plan_sha256"`
	PlanItem             string       `json:"plan_item,omitempty"`
	ExpectedConfigSHA256 string       `json:"expected_config_sha256"`
	ObservedConfigSHA256 string       `json:"observed_config_sha256,omitempty"`
	Commands             []string     `json:"commands"`
	PendingCommand       *int         `json:"pending_command,omitempty"`
	Steps                []Step       `json:"steps"`
	Error                string       `json:"error,omitempty"`
	FailedStage          Stage        `json:"failed_stage,omitempty"`
	Diagnostics          []Diagnostic `json:"diagnostics,omitempty"`
}

func (s State) clone() State {
	s.Commands = slices.Clone(s.Commands)
	s.Steps = slices.Clone(s.Steps)
	s.Diagnostics = slices.Clone(s.Diagnostics)
	if s.PendingCommand != nil {
		s.PendingCommand = new(*s.PendingCommand)
	}
	return s
}

// Plan is the capability Run consumes. C is the configuration evidence type
// the session reads back.
type Plan[C any] interface {
	Commands() []string
	// CheckBefore verifies the live configuration is the planned one and the
	// change is still eligible.
	CheckBefore(before C) error
	// CheckAfter verifies the readback and returns its fingerprint.
	CheckAfter(before, after C) (fingerprint string, err error)
}

// Session reads complete, checked configuration and writes one command.
type Session[C any] interface {
	ReadConfiguration(context.Context) (C, error)
	Write(context.Context, string) error
}

// Opener lends exactly one session for the whole operation, then returns the
// operation error and an independent cleanup error.
type Opener[C any] func(ctx context.Context, run func(context.Context, Session[C]) error) (opErr, cleanupErr error)

// Journal consumes each state synchronously and must not retain it. An error
// from a publication that became visible anyway should implement
// Committed() bool returning true, so Run treats the intent as possibly sent.
// FinalizeExecution must leave a recoverable checkpoint if it fails.
type Journal interface {
	WriteExecution(context.Context, State) error
	FinalizeExecution(context.Context, State) error
}

// Classifier maps an error to safe diagnostic codes, for example
// session.Codes. It must not return raw error text.
type Classifier func(error) []string

// Request is one execution.
type Request[C any] struct {
	Plan     Plan[C]
	Identity State // Device, PlanSHA256, PlanItem and ExpectedConfigSHA256
	Open     Opener[C]
	Journal  Journal
	Classify Classifier
}

// Result is the terminal state. ReportUnconfirmed means the final state may
// not be durable; State is then terminal-only evidence.
type Result struct {
	State             State
	ReportUnconfirmed bool
	Interrupted       bool
}

type runError struct{ cause error }

func (*runError) Error() string      { return "apply: execution failed" }
func (e *runError) Unwrap() error    { return e.cause }
func (e *runError) GoString() string { return e.Error() }

type committed interface {
	error
	Committed() bool
}

// Run executes the plan once.
func Run[C any](ctx context.Context, req Request[C]) (Result, error) {
	if req.Plan == nil || req.Open == nil || req.Journal == nil {
		return Result{}, &runError{ErrProtocol}
	}
	r := &run[C]{ctx: ctx, req: req, stage: StageConnect}
	r.state = State{
		Status:               StatusConfirmed,
		Device:               req.Identity.Device,
		PlanSHA256:           req.Identity.PlanSHA256,
		PlanItem:             req.Identity.PlanItem,
		ExpectedConfigSHA256: req.Identity.ExpectedConfigSHA256,
		Commands:             req.Plan.Commands(),
		Steps:                []Step{},
	}
	if err := ctx.Err(); err != nil {
		return r.fail(err, nil, false)
	}
	if err := req.Journal.WriteExecution(ctx, r.state.clone()); err != nil {
		r.stage = StageJournal
		return r.fail(err, nil, false)
	}
	opErr, cleanupErr := ownedOpen(ctx, req.Open, r.operation)
	if opErr != nil || cleanupErr != nil || !r.verified {
		return r.fail(cmpErr(opErr, ErrProtocol, r.verified), cleanupErr, false)
	}
	if err := ctx.Err(); err != nil {
		return r.fail(err, nil, false)
	}
	r.state.Status = StatusVerified
	if err := req.Journal.FinalizeExecution(ctx, r.state.clone()); err != nil {
		r.stage = StageJournal
		return r.fail(err, nil, true)
	}
	if err := ctx.Err(); err != nil {
		return r.fail(err, nil, true)
	}
	return Result{State: r.state.clone()}, nil
}

func cmpErr(err, fallback error, verified bool) error {
	if err == nil && !verified {
		return fallback
	}
	return err
}

type run[C any] struct {
	ctx      context.Context
	req      Request[C]
	state    State
	stage    Stage
	started  bool
	verified bool
}

func (r *run[C]) operation(opCtx context.Context, session Session[C]) error {
	if opCtx == nil || session == nil {
		return ErrProtocol
	}
	live := func() error { return errors.Join(r.ctx.Err(), opCtx.Err()) }
	if err := live(); err != nil {
		return err
	}
	r.stage = StageReadback
	before, err := session.ReadConfiguration(opCtx)
	if err != nil {
		return err
	}
	if err := r.req.Plan.CheckBefore(before); err != nil {
		return err
	}
	for i, command := range r.state.Commands {
		r.stage = StageExecute
		if err := live(); err != nil {
			return err
		}
		r.state.Status, r.state.PendingCommand = StatusExecuting, new(i)
		if err := r.req.Journal.WriteExecution(opCtx, r.state.clone()); err != nil {
			if c, ok := errors.AsType[committed](err); ok && c.Committed() {
				r.started = true
			}
			r.stage = StageJournal
			return err
		}
		r.started = true
		if err := live(); err != nil {
			return err
		}
		if err := session.Write(opCtx, command); err != nil {
			return err
		}
		r.state.Steps = append(r.state.Steps, Step{Index: i, Status: "acknowledged"})
		r.state.PendingCommand = nil
		if err := r.req.Journal.WriteExecution(context.WithoutCancel(opCtx), r.state.clone()); err != nil {
			r.stage = StageJournal
			return err
		}
		if err := live(); err != nil {
			return err
		}
	}
	r.stage = StageReadback
	after, err := session.ReadConfiguration(opCtx)
	if err != nil {
		return err
	}
	digest, err := r.req.Plan.CheckAfter(before, after)
	if err != nil {
		return err
	}
	r.state.ObservedConfigSHA256 = digest
	r.verified = true
	return live()
}

func (r *run[C]) fail(cause, cleanup error, finalization bool) (Result, error) {
	r.state.Status = StatusFailed
	if r.started {
		r.state.Status = StatusUncertain
	}
	interrupted := r.ctx.Err() != nil || errors.Is(cause, context.Canceled)
	switch {
	case interrupted:
		r.state.Error = ErrorInterrupted
	case finalization:
		r.state.Error = ErrorFinalizationUnconfirmed
	default:
		r.state.Error = ErrorExecutionFailed
	}
	r.state.FailedStage = r.stage
	r.diagnose(cause, r.stage)
	r.diagnose(r.ctx.Err(), r.stage)
	if cleanup != nil {
		r.diagnose(cleanup, StageClose)
		if !slices.ContainsFunc(r.state.Diagnostics, func(d Diagnostic) bool { return d.Stage == StageClose }) {
			r.state.Diagnostics = append(r.state.Diagnostics, Diagnostic{Code: ErrorCleanupFailed, Stage: StageClose})
		}
	}
	publication := r.req.Journal.WriteExecution(context.WithoutCancel(r.ctx), r.state.clone())
	return Result{
		State:             r.state.clone(),
		ReportUnconfirmed: publication != nil || finalization,
		Interrupted:       interrupted,
	}, &runError{errors.Join(cause, cleanup, r.ctx.Err(), publication)}
}

func (r *run[C]) diagnose(err error, stage Stage) {
	if err == nil || r.req.Classify == nil {
		return
	}
	for _, code := range r.req.Classify(err) {
		d := Diagnostic{Code: code, Stage: stage}
		if code != "" && !slices.Contains(r.state.Diagnostics, d) {
			r.state.Diagnostics = append(r.state.Diagnostics, d)
		}
	}
}

// ownedOpen admits at most one callback and waits for it before returning,
// so a faulty opener can neither replay the operation nor let an admitted
// callback outlive final publication.
func ownedOpen[C any](ctx context.Context, open Opener[C], operation func(context.Context, Session[C]) error) (error, error) {
	var mu sync.Mutex
	active, admitted := true, false
	done := make(chan struct{})
	var callbackErr, protocolErr error
	opErr, cleanupErr := open(ctx, func(opCtx context.Context, s Session[C]) error {
		mu.Lock()
		if !active || admitted {
			protocolErr = ErrProtocol
			mu.Unlock()
			return ErrProtocol
		}
		admitted = true
		mu.Unlock()
		err := ErrProtocol
		defer func() {
			mu.Lock()
			callbackErr = err
			close(done)
			mu.Unlock()
		}()
		err = operation(opCtx, s)
		return err
	})
	mu.Lock()
	active = false
	started := admitted
	mu.Unlock()
	if !started {
		return errors.Join(opErr, ErrProtocol), cleanupErr
	}
	<-done
	mu.Lock()
	defer mu.Unlock()
	return errors.Join(opErr, callbackErr, protocolErr, ctx.Err()), cleanupErr
}
