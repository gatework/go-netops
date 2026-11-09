package assess

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/gatework/go-netops/evidence"
)

// SchemaVersion is the version of the Assessment JSON document.
const SchemaVersion = 3

// RuleState is what happened to one rule during a run.
type RuleState string

const (
	RuleRan      RuleState = "ran"
	RuleSkipped  RuleState = "skipped"
	RuleDisabled RuleState = "disabled"
	RuleFailed   RuleState = "failed"
)

// RuleOutcome records how one rule contributed to an assessment.
type RuleOutcome struct {
	Rule     RuleID               `json:"rule"`
	State    RuleState            `json:"state"`
	Reason   string               `json:"reason,omitempty"`
	Missing  []evidence.Dimension `json:"missing,omitempty"`
	Findings int                  `json:"findings"`
}

// Assessment is the result of running a catalog over one model. A rule that
// did not run contributes no conclusion; consult Rules before reading the
// absence of a finding as health.
type Assessment struct {
	SchemaVersion int              `json:"schema_version"`
	Findings      []Finding        `json:"findings"`
	Rules         []RuleOutcome    `json:"rules"`
	Counts        map[Severity]int `json:"counts"`
}

// Highest returns the highest severity among the findings, or SeverityUnset.
func (a Assessment) Highest() Severity {
	h := SeverityUnset
	for _, f := range a.Findings {
		h = max(h, f.Severity)
	}
	return h
}

// Incomplete reports whether any enabled rule was skipped or failed.
func (a Assessment) Incomplete() bool {
	return slices.ContainsFunc(a.Rules, func(o RuleOutcome) bool {
		return o.State == RuleSkipped || o.State == RuleFailed
	})
}

// VerifyCommands resolves the collection command for a dimension so that
// findings can tell the operator how to re-check them. The selected vendor
// profile normally satisfies it.
type VerifyCommands interface {
	Command(evidence.Dimension) (string, error)
}

// Engine runs a catalog under a policy.
type Engine[M any] struct {
	catalog *Catalog[M]
	policy  Policy
	verify  VerifyCommands
}

// NewEngine validates the policy against the catalog. verify may be nil.
func NewEngine[M any](catalog *Catalog[M], policy Policy, verify VerifyCommands) (*Engine[M], error) {
	if catalog == nil {
		return nil, fmt.Errorf("%w: nil catalog", ErrInvalid)
	}
	if err := Validate(policy, catalog); err != nil {
		return nil, err
	}
	return &Engine[M]{catalog: catalog, policy: policy, verify: verify}, nil
}

// Run evaluates every enabled rule whose required evidence is complete. A
// panicking rule is recorded as failed and does not stop the others. On
// cancellation Run stops before the next rule and returns the partial
// assessment, with the remaining rules marked skipped, and ctx.Err().
func (e *Engine[M]) Run(ctx context.Context, model M, coverage evidence.Coverage) (Assessment, error) {
	a := Assessment{SchemaVersion: SchemaVersion, Findings: []Finding{}, Rules: []RuleOutcome{}, Counts: map[Severity]int{}}
	var cancelled error
	for rule := range e.catalog.Rules() {
		meta := rule.Meta()
		outcome := RuleOutcome{Rule: meta.ID}
		switch {
		case cancelled != nil:
			outcome.State, outcome.Reason = RuleSkipped, "cancelled"
		case !e.policy.enabled(meta.ID):
			outcome.State = RuleDisabled
		default:
			if err := ctx.Err(); err != nil {
				cancelled = err
				outcome.State, outcome.Reason = RuleSkipped, "cancelled"
				break
			}
			if missing := coverage.Missing(meta.Requires...); len(missing) > 0 {
				outcome.State, outcome.Reason, outcome.Missing = RuleSkipped, "insufficient_evidence", missing
				break
			}
			findings, err := e.evaluate(rule, meta, model)
			if err != nil {
				outcome.State, outcome.Reason = RuleFailed, err.Error()
				break
			}
			outcome.State, outcome.Findings = RuleRan, len(findings)
			a.Findings = append(a.Findings, findings...)
		}
		a.Rules = append(a.Rules, outcome)
	}
	slices.SortStableFunc(a.Findings, compareFindings)
	for _, f := range a.Findings {
		a.Counts[f.Severity]++
	}
	return a, cancelled
}

func (e *Engine[M]) evaluate(rule Rule[M], meta RuleMeta, model M) (findings []Finding, err error) {
	rp := e.policy.Rules[meta.ID]
	defer func() {
		if r := recover(); r != nil {
			findings, err = nil, fmt.Errorf("rule_panic")
		}
	}()
	raw := rule.Evaluate(Input[M]{Model: model, Params: meta.Params.overlay(rp.Params)})
	verify := e.verifyCommands(meta.Verify)
	findings = make([]Finding, 0, len(raw))
	for _, f := range raw {
		f.Rule = meta.ID
		f.Severity = cmp.Or(rp.Severity, f.Severity, meta.Severity)
		f.Category = cmp.Or(f.Category, meta.Category)
		f.Title = cmp.Or(f.Title, meta.Title)
		if len(f.Verify) == 0 {
			f.Verify = slices.Clone(verify)
		}
		if len(f.References) == 0 {
			f.References = slices.Clone(meta.References)
		}
		if !f.Severity.Valid() || !f.Category.Valid() {
			return nil, fmt.Errorf("invalid_finding")
		}
		findings = append(findings, f)
	}
	return findings, nil
}

func (e *Engine[M]) verifyCommands(dims []evidence.Dimension) []string {
	if e.verify == nil {
		return nil
	}
	var out []string
	for _, d := range dims {
		if c, err := e.verify.Command(d); err == nil && c != "" && !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	return out
}
