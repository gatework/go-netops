package evidence

import (
	"fmt"
	"slices"
	"time"
)

// Dimension names one kind of observed fact, such as "nve_peers" or "radios".
type Dimension string

// SourceRef points a fact or finding back to the device output it came from.
type SourceRef struct {
	Command string `json:"command"`
	// Digest is the SHA-256 of the raw response, when bytes were received.
	Digest string `json:"digest,omitempty"`
	// Line is the 1-based response line; 0 means the whole response.
	Line int `json:"line,omitempty"`
}

// Issue is a safe, code-based parsing or collection problem. No field ever
// carries raw device output or secrets.
type Issue struct {
	Code string `json:"code"`
	// Line is the 1-based response line; 0 means the whole response.
	Line int `json:"line,omitempty"`
	// Count is how many times the issue occurred, when aggregated.
	Count int `json:"count,omitempty"`
	// Expected and Observed are accounting totals, for example a declared
	// row count against the rows actually parsed. Nil means not applicable.
	Expected *int `json:"expected,omitempty"`
	Observed *int `json:"observed,omitempty"`
	// Fields names the fields involved, such as missing table columns.
	Fields []string `json:"fields,omitempty"`
	// Scope identifies where the issue applies, such as {"vrf": "default"}
	// or {"ap_name": "ap-1"}. Values are device identifiers, never output.
	Scope  map[string]string `json:"scope,omitempty"`
	Detail string            `json:"detail,omitempty"`
}

// Observation is the typed result of observing one dimension.
type Observation[T any] struct {
	Dimension   Dimension   `json:"dimension"`
	Status      Status      `json:"status"`
	Facts       []T         `json:"facts"`
	Issues      []Issue     `json:"issues"`
	Sources     []SourceRef `json:"sources"`
	StartedAt   *time.Time  `json:"started_at,omitempty"`
	CompletedAt *time.Time  `json:"completed_at,omitempty"`
}

// NewObservation validates and copies its inputs. Nil slices become empty so
// reports distinguish "observed nothing" from "not observed".
func NewObservation[T any](dimension Dimension, status Status, facts []T, issues []Issue, sources []SourceRef) (Observation[T], error) {
	if dimension == "" {
		return Observation[T]{}, fmt.Errorf("%w: empty dimension", ErrInvalid)
	}
	if !status.Valid() {
		return Observation[T]{}, fmt.Errorf("%w: dimension %s status %q", ErrInvalid, dimension, string(status))
	}
	if slices.ContainsFunc(issues, func(i Issue) bool { return i.Code == "" }) {
		return Observation[T]{}, fmt.Errorf("%w: dimension %s issue without code", ErrInvalid, dimension)
	}
	return Observation[T]{
		Dimension: dimension,
		Status:    status,
		Facts:     append(make([]T, 0, len(facts)), facts...),
		Issues:    append(make([]Issue, 0, len(issues)), issues...),
		Sources:   append(make([]SourceRef, 0, len(sources)), sources...),
	}, nil
}

// Complete reports whether the observation is whole.
func (o Observation[T]) Complete() bool { return o.Status.Complete() }

// Coverage reports the completeness of each observed dimension. Unknown
// dimensions are treated as not requested.
type Coverage map[Dimension]Status

// Status returns the status of d, defaulting to StatusNotRequested.
func (c Coverage) Status(d Dimension) Status {
	if s, ok := c[d]; ok {
		return s
	}
	return StatusNotRequested
}

// Missing returns the dimensions in want that are not complete, in order.
func (c Coverage) Missing(want ...Dimension) []Dimension {
	var missing []Dimension
	for _, d := range want {
		if !c.Status(d).Complete() {
			missing = append(missing, d)
		}
	}
	return missing
}

// Complete reports whether at least one dimension was requested and every
// requested dimension is complete.
func (c Coverage) Complete() bool {
	requested := 0
	for _, s := range c {
		if s == StatusNotRequested {
			continue
		}
		requested++
		if !s.Complete() {
			return false
		}
	}
	return requested > 0
}

// Record adds an observation's status to the coverage.
func Record[T any](c Coverage, o Observation[T]) {
	c[o.Dimension] = o.Status
}
