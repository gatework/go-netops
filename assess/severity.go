// Package assess evaluates a typed domain model against a catalog of rules
// and produces findings ranked by severity.
//
// Rules declare the evidence dimensions they require. The engine skips a rule
// whose evidence is incomplete instead of letting it conclude anything, so an
// absent finding never means "healthy" unless the rule actually ran.
package assess

import (
	"errors"
	"fmt"
)

// ErrInvalid reports an invalid rule, catalog, policy or finding.
var ErrInvalid = errors.New("assess: invalid")

// Severity ranks the operational risk of a finding. The zero value means
// "use the rule's default" and never appears in an assessment.
type Severity int

// Severities, lowest first.
const (
	SeverityUnset Severity = iota
	SeverityInfo
	SeverityLow
	SeverityMedium
	SeverityHigh
	SeverityCritical
)

var severityNames = [...]string{"", "info", "low", "medium", "high", "critical"}

// Severities lists the assignable severities from highest to lowest.
func Severities() []Severity {
	return []Severity{SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo}
}

// Valid reports whether s is an assignable severity.
func (s Severity) Valid() bool { return s >= SeverityInfo && s <= SeverityCritical }

func (s Severity) String() string {
	if s < 0 || int(s) >= len(severityNames) {
		return fmt.Sprintf("Severity(%d)", int(s))
	}
	return severityNames[s]
}

// ParseSeverity accepts a lowercase severity name.
func ParseSeverity(name string) (Severity, error) {
	for i, n := range severityNames {
		if n != "" && n == name {
			return Severity(i), nil
		}
	}
	return SeverityUnset, fmt.Errorf("%w: severity %q", ErrInvalid, name)
}

// MarshalText encodes a valid severity by name.
func (s Severity) MarshalText() ([]byte, error) {
	if !s.Valid() {
		return nil, fmt.Errorf("%w: severity %d", ErrInvalid, int(s))
	}
	return []byte(s.String()), nil
}

// UnmarshalText decodes a severity name and rejects unknown names.
func (s *Severity) UnmarshalText(text []byte) error {
	v, err := ParseSeverity(string(text))
	if err != nil {
		return err
	}
	*s = v
	return nil
}

// Category groups findings by the kind of operational concern.
type Category string

const (
	// CategoryEvidence means facts are missing, partial or contradictory.
	CategoryEvidence Category = "evidence"
	// CategoryConfiguration means a configuration is internally inconsistent.
	CategoryConfiguration Category = "configuration"
	// CategoryConsistency means peers that should agree do not.
	CategoryConsistency Category = "consistency"
	// CategoryDrift means state deviates from a baseline or a previous snapshot.
	CategoryDrift Category = "drift"
	// CategoryRuntime covers operational state such as sessions, peers or links.
	CategoryRuntime Category = "runtime"
	// CategorySecurity means weak or missing protection.
	CategorySecurity Category = "security"
	// CategoryCapacity covers load, utilization or resource limits.
	CategoryCapacity Category = "capacity"
)

// Valid reports whether c is a declared category.
func (c Category) Valid() bool {
	switch c {
	case CategoryEvidence, CategoryConfiguration, CategoryConsistency, CategoryDrift,
		CategoryRuntime, CategorySecurity, CategoryCapacity:
		return true
	}
	return false
}
