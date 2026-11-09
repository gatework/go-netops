// Package archcheck enforces package import boundaries declared as data.
//
// Rules are matched against the production import graph from `go list
// -json`, so test-only imports never count. Patterns starting with "./"
// name packages of the checked module ("./parser", "./vendors/...", "." for
// the root); any other pattern is an import path ("os",
// "github.com/spf13/..."). A trailing "/..." matches the package and every
// package below it.
package archcheck

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Rule constrains the imports of a set of packages.
type Rule struct {
	Name string `yaml:"name"`
	// Packages selects the constrained local packages; Except removes some.
	Packages []string `yaml:"packages"`
	Except   []string `yaml:"except"`
	// Deny lists forbidden imports; Allow re-permits some of them.
	Deny  []string `yaml:"deny"`
	Allow []string `yaml:"allow"`
	// OnlyLocal, when set, restricts local imports to these patterns.
	OnlyLocal []string `yaml:"only_local"`
	// Transitive checks every dependency instead of direct imports only.
	Transitive bool `yaml:"transitive"`
}

// Config is a rule table.
type Config struct {
	// Declared, when set, requires every local package to match a pattern,
	// so new packages cannot silently escape the rules.
	Declared []string `yaml:"declared"`
	Rules    []Rule   `yaml:"rules"`
}

// Package is the subset of `go list -json` output the checker reads.
type Package struct {
	ImportPath string
	Imports    []string
	Deps       []string
}

// Violation is one broken rule.
type Violation struct {
	Rule, Package, Import string
}

func (v Violation) String() string {
	if v.Import == "" {
		return fmt.Sprintf("%s: %s", v.Rule, v.Package)
	}
	return fmt.Sprintf("%s: %s -> %s", v.Rule, v.Package, v.Import)
}

// DecodeConfig strictly decodes a YAML rule table.
func DecodeConfig(body []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(body))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("archcheck: rules: %w", err)
	}
	for i, r := range c.Rules {
		if r.Name == "" || len(r.Packages) == 0 || len(r.Deny) == 0 && len(r.OnlyLocal) == 0 {
			return Config{}, fmt.Errorf("archcheck: rule %d needs name, packages and deny or only_local", i+1)
		}
		for _, p := range slices.Concat(r.Packages, r.Except, r.OnlyLocal) {
			if !strings.HasPrefix(p, "./") && p != "." {
				return Config{}, fmt.Errorf("archcheck: rule %q: %q must be a local pattern", r.Name, p)
			}
		}
	}
	return c, nil
}

// DecodePackages reads a stream of `go list -json` objects.
func DecodePackages(r io.Reader) ([]Package, error) {
	var out []Package
	d := json.NewDecoder(r)
	for {
		var p Package
		err := d.Decode(&p)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("archcheck: go list output: %w", err)
		}
		out = append(out, p)
	}
}

// Check evaluates the rules for packages of module.
func Check(c Config, module string, packages []Package) []Violation {
	local := func(path string) (string, bool) {
		if path == module {
			return ".", true
		}
		if rel, ok := strings.CutPrefix(path, module+"/"); ok {
			return "./" + rel, true
		}
		return "", false
	}
	var out []Violation
	for _, p := range packages {
		rel, ok := local(p.ImportPath)
		if !ok {
			continue
		}
		if len(c.Declared) > 0 && !matchAny(c.Declared, rel) {
			out = append(out, Violation{Rule: "undeclared package", Package: rel})
		}
		for _, r := range c.Rules {
			if !matchAny(r.Packages, rel) || matchAny(r.Except, rel) {
				continue
			}
			imports := p.Imports
			if r.Transitive {
				imports = p.Deps
			}
			for _, imp := range imports {
				name := imp
				localImp, isLocal := local(imp)
				if isLocal {
					name = localImp
				}
				denied := matchAny(r.Deny, name) && !matchAny(r.Allow, name)
				outside := isLocal && len(r.OnlyLocal) > 0 && !matchAny(r.OnlyLocal, name)
				if denied || outside {
					out = append(out, Violation{Rule: r.Name, Package: rel, Import: name})
				}
			}
		}
	}
	slices.SortFunc(out, func(a, b Violation) int { return strings.Compare(a.String(), b.String()) })
	return slices.Compact(out)
}

func matchAny(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool { return match(p, name) })
}

func match(pattern, name string) bool {
	if pattern == "./..." {
		return name == "." || strings.HasPrefix(name, "./")
	}
	if base, ok := strings.CutSuffix(pattern, "/..."); ok {
		return name == base || strings.HasPrefix(name, base+"/")
	}
	return pattern == name
}
