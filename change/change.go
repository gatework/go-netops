// Package change models typed configuration changes and immutable plans.
//
// A Change is one business increment over a domain model M, such as "bind ACL
// X to SVI Vlan10". It renders its own device commands and decides whether
// the current model allows it (Eligible) and whether the model now reflects
// it (Satisfied). A Plan binds a change to one device and to the exact
// configuration fingerprint the change was planned against.
package change

import (
	"errors"
	"regexp"

	"github.com/gatework/go-netops/drift"
)

var (
	// ErrInvalidPlan reports a malformed change or plan.
	ErrInvalidPlan = errors.New("change: invalid plan")
	// ErrDrifted reports that the configuration changed since planning.
	ErrDrifted = errors.New("change: configuration drifted since planning")
	// ErrNotEligible reports that the current model does not allow the change.
	ErrNotEligible = errors.New("change: not eligible")
	// ErrNotSatisfied reports that readback does not reflect the change.
	ErrNotSatisfied = errors.New("change: readback does not satisfy change")
	// ErrNotPreserved reports readback changes beyond the planned change.
	ErrNotPreserved = errors.New("change: readback changed more than planned")
)

// Kind identifies a change type, such as "svi_acl".
type Kind string

var kindPattern = regexp.MustCompile(`\A[a-z][a-z0-9_]{0,63}\z`)

// Valid reports whether k is a well-formed kind.
func (k Kind) Valid() bool { return kindPattern.MatchString(string(k)) }

// Script is the rendered device commands and their rollback.
type Script struct {
	Commands []string `json:"commands"`
	Rollback []string `json:"rollback"`
}

// Change is one typed configuration increment over model M.
type Change[M any] interface {
	Kind() Kind
	// Validate checks the change in isolation, without a model.
	Validate() error
	// Render returns the exact commands; it must be deterministic.
	Render() (Script, error)
	// Eligible reports whether the change applies to the current model.
	Eligible(M) error
	// Satisfied reports whether the model reflects the applied change.
	Satisfied(M) error
}

// Preserving is implemented by changes that can also prove nothing besides
// their target changed. Plan.Verify uses it when present, so a readback
// that satisfies the change but altered unrelated configuration fails.
type Preserving[M any] interface {
	Preserved(before, after M) error
}

// Corrector proposes a change that would remove a deviation. ok is false when
// the deviation needs operator judgment and has no automatic correction.
type Corrector[M any] interface {
	Correct(model M, deviation drift.Deviation) (c Change[M], ok bool)
}
