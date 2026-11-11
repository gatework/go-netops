package assess

import (
	"fmt"
	"iter"
	"regexp"

	"github.com/gatework/go-netops/evidence"
)

// RuleID is a stable, lowercase identifier such as "vxlan.nve_peer_down".
type RuleID string

var ruleIDPattern = regexp.MustCompile(`\A[a-z][a-z0-9_]*(?:\.[a-z][a-z0-9_]*)*\z`)

// RuleMeta is everything about a rule that is not evaluation logic. Keeping
// titles, default severity and evidence needs here, rather than inline in the
// rule body, lets policy and reports treat every rule uniformly.
type RuleMeta struct {
	ID       RuleID
	Title    string
	Severity Severity
	Category Category
	// Requires lists dimensions that must be complete for the rule to run.
	Requires []evidence.Dimension
	// Verify lists dimensions whose collection commands confirm the finding.
	Verify     []evidence.Dimension
	References []string
	// Params declares tunable thresholds and their defaults.
	Params Params
}

func (m RuleMeta) validate() error {
	switch {
	case !ruleIDPattern.MatchString(string(m.ID)):
		return fmt.Errorf("%w: rule id %q", ErrInvalid, m.ID)
	case m.Title == "":
		return fmt.Errorf("%w: rule %s has no title", ErrInvalid, m.ID)
	case !m.Severity.Valid():
		return fmt.Errorf("%w: rule %s default severity", ErrInvalid, m.ID)
	case !m.Category.Valid():
		return fmt.Errorf("%w: rule %s category %q", ErrInvalid, m.ID, m.Category)
	}
	return nil
}

// Input is what a rule receives: the model and its effective parameters,
// which are the rule defaults overlaid by policy.
type Input[M any] struct {
	Model  M
	Params Params
}

// Rule evaluates one concern over a domain model M. Evaluate must not modify
// the model and must be safe to call concurrently with other rules.
type Rule[M any] interface {
	Meta() RuleMeta
	Evaluate(in Input[M]) []Finding
}

type funcRule[M any] struct {
	meta RuleMeta
	eval func(Input[M]) []Finding
}

func (r funcRule[M]) Meta() RuleMeta                 { return r.meta }
func (r funcRule[M]) Evaluate(in Input[M]) []Finding { return r.eval(in) }

// NewRule adapts a function into a Rule.
//
//nolint:iface // catalogs hold rules only through the Rule interface; funcRule is an implementation detail
func NewRule[M any](meta RuleMeta, eval func(Input[M]) []Finding) Rule[M] {
	return funcRule[M]{meta: meta, eval: eval}
}

// Catalog is an explicitly constructed, immutable set of rules.
type Catalog[M any] struct {
	rules []Rule[M]
	index map[RuleID]int
}

// NewCatalog validates rule metadata and rejects duplicate identifiers.
func NewCatalog[M any](rules ...Rule[M]) (*Catalog[M], error) {
	c := &Catalog[M]{index: make(map[RuleID]int, len(rules))}
	for _, r := range rules {
		if r == nil {
			return nil, fmt.Errorf("%w: nil rule", ErrInvalid)
		}
		meta := r.Meta()
		if err := meta.validate(); err != nil {
			return nil, err
		}
		if _, dup := c.index[meta.ID]; dup {
			return nil, fmt.Errorf("%w: duplicate rule %s", ErrInvalid, meta.ID)
		}
		c.index[meta.ID] = len(c.rules)
		c.rules = append(c.rules, r)
	}
	return c, nil
}

// Rules iterates the rules in declaration order.
func (c *Catalog[M]) Rules() iter.Seq[Rule[M]] {
	return func(yield func(Rule[M]) bool) {
		for _, r := range c.rules {
			if !yield(r) {
				return
			}
		}
	}
}

// Lookup returns the rule with the given id.
func (c *Catalog[M]) Lookup(id RuleID) (Rule[M], bool) {
	i, ok := c.index[id]
	if !ok {
		return nil, false
	}
	return c.rules[i], true
}

// Len returns the number of rules.
func (c *Catalog[M]) Len() int { return len(c.rules) }
