// Package evidence holds collection facts and their completeness.
//
// A complete observation means the evidence is whole, not that the network is
// healthy. Rules must treat any other status as insufficient to conclude.
package evidence

import (
	"errors"
	"fmt"
)

// ErrInvalid reports evidence that violates its structural contract.
var ErrInvalid = errors.New("evidence: invalid")

// Status is the completeness of one observed dimension.
type Status string

// Evidence statuses.
const (
	StatusComplete     Status = "complete"
	StatusPartial      Status = "partial"
	StatusFailed       Status = "failed"
	StatusUnsupported  Status = "unsupported"
	StatusNotRequested Status = "not_requested"
)

// Statuses lists every valid status in severity order of missing evidence.
func Statuses() []Status {
	return []Status{StatusComplete, StatusPartial, StatusFailed, StatusUnsupported, StatusNotRequested}
}

// Valid reports whether s is one of the declared statuses.
func (s Status) Valid() bool {
	switch s {
	case StatusComplete, StatusPartial, StatusFailed, StatusUnsupported, StatusNotRequested:
		return true
	}
	return false
}

// Complete reports whether the evidence is whole.
func (s Status) Complete() bool { return s == StatusComplete }

// MarshalText rejects undeclared statuses so they never reach a report.
func (s Status) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("%w: status %q", ErrInvalid, string(s))
	}
	return []byte(s), nil
}

// UnmarshalText accepts only declared statuses.
func (s *Status) UnmarshalText(text []byte) error {
	v := Status(text)
	if !v.Valid() {
		return fmt.Errorf("%w: status %q", ErrInvalid, string(text))
	}
	*s = v
	return nil
}
