package assess

import (
	"bytes"
	"fmt"
	"maps"
	"slices"

	"go.yaml.in/yaml/v3"
)

// Params holds rule thresholds. Values are YAML scalars or lists; typed
// accessors fall back to the default when a value is absent or mistyped, and
// Policy.Validate rejects mistyped overrides before any rule runs.
type Params map[string]any

// Float returns a numeric parameter.
func (p Params) Float(name string, def float64) float64 {
	switch v := p[name].(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	}
	return def
}

// Int returns an integral parameter.
func (p Params) Int(name string, def int) int {
	switch v := p[name].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		if v == float64(int(v)) {
			return int(v)
		}
	}
	return def
}

// Bool returns a boolean parameter.
func (p Params) Bool(name string, def bool) bool {
	if v, ok := p[name].(bool); ok {
		return v
	}
	return def
}

// String returns a string parameter.
func (p Params) String(name, def string) string {
	if v, ok := p[name].(string); ok {
		return v
	}
	return def
}

// Strings returns a list-of-strings parameter.
func (p Params) Strings(name string, def []string) []string {
	switch v := p[name].(type) {
	case []string:
		return slices.Clone(v)
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := e.(string)
			if !ok {
				return def
			}
			out = append(out, s)
		}
		return out
	}
	return def
}

// Ints returns a list-of-integers parameter.
func (p Params) Ints(name string, def []int) []int {
	switch v := p[name].(type) {
	case []int:
		return slices.Clone(v)
	case []any:
		out := make([]int, 0, len(v))
		for _, e := range v {
			n, ok := e.(int)
			if !ok {
				return def
			}
			out = append(out, n)
		}
		return out
	}
	return def
}

// overlay returns base with overrides applied, without modifying either.
func (p Params) overlay(overrides Params) Params {
	out := maps.Clone(p)
	if out == nil {
		out = Params{}
	}
	maps.Copy(out, overrides)
	return out
}

// RulePolicy adjusts one rule.
type RulePolicy struct {
	Enabled  *bool    `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Severity Severity `yaml:"severity,omitempty" json:"severity,omitempty"`
	Params   Params   `yaml:"params,omitempty" json:"params,omitempty"`
}

// Policy selects and tunes rules. The zero Policy runs every rule with its
// defaults.
type Policy struct {
	// OnlyEnabled runs only rules whose policy sets enabled: true.
	OnlyEnabled bool                  `yaml:"only_enabled,omitempty" json:"only_enabled,omitempty"`
	Rules       map[RuleID]RulePolicy `yaml:"rules,omitempty" json:"rules,omitempty"`
}

// DecodePolicy strictly decodes a YAML policy document. Unknown fields are
// rejected; an empty document yields the zero policy.
func DecodePolicy(body []byte) (Policy, error) {
	var p Policy
	if len(bytes.TrimSpace(body)) == 0 {
		return p, nil
	}
	decoder := yaml.NewDecoder(bytes.NewReader(body))
	decoder.KnownFields(true)
	if err := decoder.Decode(&p); err != nil {
		return Policy{}, fmt.Errorf("%w: policy: %w", ErrInvalid, err)
	}
	return p, nil
}

// UnmarshalYAML decodes a severity name.
func (s *Severity) UnmarshalYAML(node *yaml.Node) error {
	return s.UnmarshalText([]byte(node.Value))
}

// MarshalYAML encodes a severity name.
func (s Severity) MarshalYAML() (any, error) {
	if s == SeverityUnset {
		return nil, nil //nolint:nilnil // an unset severity encodes as YAML null
	}
	return s.String(), nil
}

// Validate checks the policy against a catalog: every named rule must exist
// and every parameter override must be declared with a compatible type.
func Validate[M any](p Policy, c *Catalog[M]) error {
	for _, id := range slices.Sorted(maps.Keys(p.Rules)) {
		rule, ok := c.Lookup(id)
		if !ok {
			return fmt.Errorf("%w: policy names unknown rule %s", ErrInvalid, id)
		}
		rp := p.Rules[id]
		if rp.Severity != SeverityUnset && !rp.Severity.Valid() {
			return fmt.Errorf("%w: policy rule %s severity", ErrInvalid, id)
		}
		declared := rule.Meta().Params
		for _, name := range slices.Sorted(maps.Keys(rp.Params)) {
			def, ok := declared[name]
			if !ok {
				return fmt.Errorf("%w: rule %s has no parameter %q", ErrInvalid, id, name)
			}
			if !sameKind(def, rp.Params[name]) {
				return fmt.Errorf("%w: rule %s parameter %q type", ErrInvalid, id, name)
			}
		}
	}
	return nil
}

func sameKind(def, v any) bool {
	kind := func(x any) string {
		switch x.(type) {
		case int, int64, float64:
			return "number"
		case bool:
			return "bool"
		case string:
			return "string"
		case []string, []int, []any:
			return "list"
		}
		return "other"
	}
	return kind(def) == kind(v)
}

func (p Policy) enabled(id RuleID) bool {
	rp, ok := p.Rules[id]
	if ok && rp.Enabled != nil {
		return *rp.Enabled
	}
	return !p.OnlyEnabled
}
