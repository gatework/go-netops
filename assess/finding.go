package assess

import (
	"cmp"
	"slices"
	"strings"

	"github.com/gatework/go-netops/evidence"
)

// Subject identifies what a finding is about, from the most specific object
// up to its container: device 10.0.0.1 in VNI 10010 of fabric "dc1".
type Subject struct {
	Kind   string   `json:"kind"`
	Key    string   `json:"key"`
	Parent *Subject `json:"parent,omitempty"`
}

// NewSubject returns a subject without a parent.
func NewSubject(kind, key string) Subject { return Subject{Kind: kind, Key: key} }

// Child returns a subject nested under s.
func (s Subject) Child(kind, key string) Subject {
	parent := s
	return Subject{Kind: kind, Key: key, Parent: &parent}
}

// String renders the path from the outermost container, for example
// "fabric:dc1/vni:10010/device:10.0.0.1".
func (s Subject) String() string {
	var parts []string
	for cur := &s; cur != nil; cur = cur.Parent {
		parts = append(parts, cur.Kind+":"+cur.Key)
	}
	var b strings.Builder
	for i, part := range slices.Backward(parts) {
		b.WriteString(part)
		if i > 0 {
			b.WriteByte('/')
		}
	}
	return b.String()
}

// RemediationStatus states whether a remediation can be executed.
type RemediationStatus string

const (
	// RemediationCandidate has a validated change that may become a plan.
	RemediationCandidate RemediationStatus = "candidate"
	// RemediationManual needs operator judgment; commands are advisory only.
	RemediationManual RemediationStatus = "manual"
	// RemediationBlocked has unresolved prerequisites; nothing is proposed.
	RemediationBlocked RemediationStatus = "blocked"
)

// Remediation describes how to correct a finding. Only a candidate carries a
// Change that a consumer may turn into an executable plan.
type Remediation struct {
	Status        RemediationStatus `json:"status"`
	Change        any               `json:"change,omitempty"`
	Commands      []string          `json:"commands,omitempty"`
	Rollback      []string          `json:"rollback,omitempty"`
	Prerequisites []string          `json:"prerequisites,omitempty"`
	Blockers      []string          `json:"blockers,omitempty"`
	Impact        string            `json:"impact,omitempty"`
	Expected      string            `json:"expected,omitempty"`
}

// Finding is one detected risk. Rules normally fill Subject, Detail,
// Expected, Actual and Evidence; the engine fills Rule, Severity, Category,
// Title and Verify from the rule's metadata and policy.
type Finding struct {
	Rule        RuleID               `json:"rule"`
	Severity    Severity             `json:"severity"`
	Category    Category             `json:"category"`
	Subject     Subject              `json:"subject"`
	Title       string               `json:"title"`
	Detail      string               `json:"detail,omitempty"`
	Expected    any                  `json:"expected,omitempty"`
	Actual      any                  `json:"actual,omitempty"`
	Evidence    []evidence.SourceRef `json:"evidence,omitempty"`
	Verify      []string             `json:"verify,omitempty"`
	References  []string             `json:"references,omitempty"`
	Remediation *Remediation         `json:"remediation,omitempty"`
}

// compareFindings orders by severity (highest first), category, subject and
// rule, giving reports a stable, risk-first order.
func compareFindings(a, b Finding) int {
	return cmp.Or(
		cmp.Compare(b.Severity, a.Severity),
		cmp.Compare(a.Category, b.Category),
		cmp.Compare(a.Subject.String(), b.Subject.String()),
		cmp.Compare(a.Rule, b.Rule),
	)
}
