package session_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/gatework/go-expect"
	"github.com/gatework/go-net-connector/connector"
	"github.com/gatework/go-netops/session"
)

type coded struct{}

func (coded) Error() string       { return "secret device output" }
func (coded) FailureCode() string { return "invalid_command_result" }

func TestCodes(t *testing.T) {
	cases := []struct {
		err  error
		want []string
	}{
		{nil, nil},
		{context.Canceled, []string{"cancelled"}},
		{fmt.Errorf("wrap: %w", context.DeadlineExceeded), []string{"deadline_exceeded"}},
		{expect.ErrTimeout, []string{"deadline_exceeded"}},
		{connector.ErrBusy, []string{"session_busy"}},
		{connector.ErrNotConnected, []string{"session_closed"}},
		{connector.ErrScope, []string{"session_scope_ended"}},
		{&connector.Error{Code: "authentication_failed", Phase: "login"}, []string{"authentication_failed"}},
		{errors.Join(coded{}, connector.ErrClosed, errors.New("x"), errors.New("y")), []string{"invalid_command_result", "session_closed", "command_failed"}},
	}
	for _, c := range cases {
		if got := session.Codes(c.err); !slices.Equal(got, c.want) {
			t.Errorf("Codes(%v) = %v, want %v", c.err, got, c.want)
		}
	}
	if session.Code(errors.Join(context.Canceled, connector.ErrBusy)) != "cancelled" || !session.Interrupted(fmt.Errorf("x: %w", expect.ErrTimeout)) {
		t.Fatal("Code/Interrupted")
	}
}
