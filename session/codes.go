// Package session maps transport and session errors to stable failure codes.
//
// Codes are safe to persist and display: they never contain device output,
// credentials or error text. This replaces the two incompatible code sets
// go-vxlan and go-wireless maintained separately.
package session

import (
	"context"
	"errors"

	"github.com/gatework/go-expect"
	"github.com/gatework/go-net-connector/connector"
)

// Stable codes. Connector errors otherwise keep their own Code.
const (
	CodeCancelled         = "cancelled"
	CodeDeadlineExceeded  = "deadline_exceeded"
	CodeSessionClosed     = "session_closed"
	CodeSessionBusy       = "session_busy"
	CodeSessionScopeEnded = "session_scope_ended"
	CodeCommandFailed     = "command_failed"
)

// Coded is implemented by domain errors that already know their code.
type Coded interface{ FailureCode() string }

// Code returns the single most relevant code for err, or "" for nil.
func Code(err error) string {
	if codes := Codes(err); len(codes) > 0 {
		return codes[0]
	}
	return ""
}

// Codes returns one code per independent cause in err's tree, in order,
// without duplicates. Classified errors (connector errors and Coded) stop the
// traversal; joins and plain wrappers are followed.
func Codes(err error) []string {
	var codes []string
	add := func(c string) {
		for _, have := range codes {
			if have == c {
				return
			}
		}
		codes = append(codes, c)
	}
	var visit func(error)
	visit = func(err error) {
		if err == nil {
			return
		}
		if c := direct(err); c != "" {
			add(c)
			return
		}
		switch w := err.(type) {
		case interface{ Unwrap() []error }:
			for _, cause := range w.Unwrap() {
				visit(cause)
			}
			return
		case interface{ Unwrap() error }:
			if cause := w.Unwrap(); cause != nil {
				visit(cause)
				return
			}
		}
		add(CodeCommandFailed)
	}
	visit(err)
	return codes
}

func direct(err error) string {
	switch e := err.(type) {
	case Coded:
		if c := e.FailureCode(); c != "" {
			return c
		}
	case *connector.Error:
		if e != nil && e.Code != "" {
			return e.Code
		}
	}
	switch {
	case err == context.Canceled:
		return CodeCancelled
	case err == context.DeadlineExceeded, err == expect.ErrTimeout:
		return CodeDeadlineExceeded
	case err == connector.ErrClosed, err == connector.ErrNotConnected:
		return CodeSessionClosed
	case err == connector.ErrBusy:
		return CodeSessionBusy
	case err == connector.ErrScope:
		return CodeSessionScopeEnded
	}
	return ""
}

// Interrupted reports whether err was caused by cancellation or a deadline.
func Interrupted(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, expect.ErrTimeout)
}
