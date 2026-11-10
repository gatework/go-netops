package change

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/gatework/go-netops/evidence"
)

// PlanSchemaVersion is the version of the plan Envelope.
const PlanSchemaVersion = 3

// Target is the device a plan is bound to.
type Target struct {
	Host     string `json:"host"`
	Name     string `json:"name,omitempty"`
	Platform string `json:"platform"`
}

func (t Target) validate() error {
	addr, err := netip.ParseAddr(t.Host)
	if err != nil || addr.String() != t.Host || addr.Zone() != "" {
		return fmt.Errorf("%w: target host", ErrInvalidPlan)
	}
	if t.Platform == "" {
		return fmt.Errorf("%w: target platform", ErrInvalidPlan)
	}
	return nil
}

// Plan is an immutable change bound to a device and to the fingerprint of
// the configuration it was planned against.
type Plan[M any] struct {
	target   Target
	expected string
	change   Change[M]
	script   Script
}

// NewPlan validates and renders the change. Commands must be non-empty and
// free of control characters so that a plan never smuggles extra input.
func NewPlan[M any](target Target, expectedFingerprint string, c Change[M]) (*Plan[M], error) {
	if err := target.validate(); err != nil {
		return nil, err
	}
	if !evidence.ValidDigest(expectedFingerprint) {
		return nil, fmt.Errorf("%w: expected configuration fingerprint", ErrInvalidPlan)
	}
	if c == nil || !c.Kind().Valid() {
		return nil, fmt.Errorf("%w: change kind", ErrInvalidPlan)
	}
	if err := c.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPlan, err)
	}
	script, err := c.Render()
	if err != nil {
		return nil, fmt.Errorf("%w: render: %w", ErrInvalidPlan, err)
	}
	if len(script.Commands) == 0 || !safeLines(script.Commands) || !safeLines(script.Rollback) {
		return nil, fmt.Errorf("%w: rendered commands", ErrInvalidPlan)
	}
	return &Plan[M]{
		target:   target,
		expected: expectedFingerprint,
		change:   c,
		script:   Script{Commands: slices.Clone(script.Commands), Rollback: slices.Clone(script.Rollback)},
	}, nil
}

func safeLines(lines []string) bool {
	for _, l := range lines {
		if strings.TrimSpace(l) == "" || strings.ContainsFunc(l, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			return false
		}
	}
	return true
}

func (p *Plan[M]) Target() Target              { return p.target }
func (p *Plan[M]) ExpectedFingerprint() string { return p.expected }
func (p *Plan[M]) Change() Change[M]           { return p.change }
func (p *Plan[M]) Kind() Kind                  { return p.change.Kind() }
func (p *Plan[M]) Commands() []string          { return slices.Clone(p.script.Commands) }
func (p *Plan[M]) Rollback() []string          { return slices.Clone(p.script.Rollback) }
func (p *Plan[M]) String() string              { return fmt.Sprintf("Plan{%s %s}", p.change.Kind(), p.target.Host) }
func (p *Plan[M]) GoString() string            { return p.String() }

// CheckBefore requires the live configuration to be exactly the one planned
// against, and the change to still be eligible.
func (p *Plan[M]) CheckBefore(model M, fingerprint string) error {
	if fingerprint != p.expected {
		return ErrDrifted
	}
	if err := p.change.Eligible(model); err != nil {
		return fmt.Errorf("%w: %w", ErrNotEligible, err)
	}
	return nil
}

// CheckAfter requires the readback to reflect the change.
func (p *Plan[M]) CheckAfter(model M) error {
	if err := p.change.Satisfied(model); err != nil {
		return fmt.Errorf("%w: %w", ErrNotSatisfied, err)
	}
	return nil
}

// Verify checks a readback against the configuration read before sending:
// the change is satisfied and, for Preserving changes, nothing else moved.
// The before configuration must still be eligible, so a stale or swapped
// "before" cannot vouch for the readback.
func (p *Plan[M]) Verify(before, after M) error {
	if err := p.change.Eligible(before); err != nil {
		return fmt.Errorf("%w: before: %w", ErrNotEligible, err)
	}
	if err := p.CheckAfter(after); err != nil {
		return err
	}
	if pr, ok := p.change.(Preserving[M]); ok {
		if err := pr.Preserved(before, after); err != nil {
			return fmt.Errorf("%w: %w", ErrNotPreserved, err)
		}
	}
	return nil
}

// Envelope is the persisted, operator-reviewable form of a plan.
type Envelope struct {
	SchemaVersion       int             `json:"schema_version"`
	Target              Target          `json:"target"`
	ExpectedFingerprint string          `json:"expected_config_sha256"`
	Kind                Kind            `json:"kind"`
	Change              json.RawMessage `json:"change"`
	Commands            []string        `json:"commands"`
	Rollback            []string        `json:"rollback"`
}

// Envelope returns the persisted form of the plan.
func (p *Plan[M]) Envelope() (Envelope, error) {
	body, err := json.Marshal(p.change)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: encode change: %w", ErrInvalidPlan, err)
	}
	return Envelope{
		SchemaVersion:       PlanSchemaVersion,
		Target:              p.target,
		ExpectedFingerprint: p.expected,
		Kind:                p.change.Kind(),
		Change:              body,
		Commands:            p.Commands(),
		Rollback:            p.Rollback(),
	}, nil
}

// MarshalJSON encodes the plan as its Envelope.
func (p *Plan[M]) MarshalJSON() ([]byte, error) {
	e, err := p.Envelope()
	if err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Decoder rebuilds a typed change from its kind and JSON body. It returns an
// error for kinds the consumer does not allow to execute.
type Decoder[M any] func(kind Kind, body json.RawMessage) (Change[M], error)

// DecodePlan strictly decodes an envelope, rebuilds the change and re-renders
// it. The stored commands and rollback must equal the re-rendered ones, so an
// edited plan file cannot send anything the change would not produce.
func DecodePlan[M any](body []byte, decode Decoder[M]) (*Plan[M], error) {
	if decode == nil {
		return nil, fmt.Errorf("%w: no decoder", ErrInvalidPlan)
	}
	var e Envelope
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil || d.More() {
		return nil, fmt.Errorf("%w: envelope", ErrInvalidPlan)
	}
	if e.SchemaVersion != PlanSchemaVersion {
		return nil, fmt.Errorf("%w: schema_version %d", ErrInvalidPlan, e.SchemaVersion)
	}
	c, err := decode(e.Kind, e.Change)
	if err != nil {
		return nil, fmt.Errorf("%w: change: %w", ErrInvalidPlan, err)
	}
	if c == nil || c.Kind() != e.Kind {
		return nil, fmt.Errorf("%w: change kind", ErrInvalidPlan)
	}
	p, err := NewPlan(e.Target, e.ExpectedFingerprint, c)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(p.script.Commands, e.Commands) || !slices.Equal(p.script.Rollback, e.Rollback) {
		return nil, fmt.Errorf("%w: commands differ from rendered change", ErrInvalidPlan)
	}
	return p, nil
}
