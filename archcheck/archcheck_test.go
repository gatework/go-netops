package archcheck_test

import (
	"strings"
	"testing"

	"github.com/gatework/go-netops/archcheck"
)

const module = "example.com/m"

const rules = `
declared: [., ./parser, ./vendors/..., ./review]
rules:
  - name: parser stays vendor neutral
    packages: [./parser]
    deny: [./vendors/..., os]
  - name: review uses only parser
    packages: [./review]
    only_local: [./parser]
  - name: no cobra below cli
    packages: [./...]
    deny: [github.com/spf13/...]
    allow: [github.com/spf13/pflag]
    transitive: true
`

func pkg(path string, imports, deps []string) archcheck.Package {
	return archcheck.Package{ImportPath: path, Imports: imports, Deps: deps}
}

func check(t *testing.T, packages ...archcheck.Package) []string {
	t.Helper()
	c, err := archcheck.DecodeConfig([]byte(rules))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, v := range archcheck.Check(c, module, packages) {
		out = append(out, v.String())
	}
	return out
}

func TestCleanGraphPasses(t *testing.T) {
	got := check(t,
		pkg(module, nil, nil),
		pkg(module+"/parser", []string{"strings"}, []string{"strings"}),
		pkg(module+"/vendors/nxos", []string{module + "/parser", "os"}, []string{"github.com/spf13/pflag"}),
		pkg(module+"/review", []string{module + "/parser", "fmt"}, nil),
	)
	if len(got) != 0 {
		t.Fatal(got)
	}
}

func TestViolationsAreReported(t *testing.T) {
	got := check(t,
		pkg(module+"/parser", []string{module + "/vendors/nxos", "os"}, nil),
		pkg(module+"/review", []string{module + "/vendors", module + "/parser"}, nil),
		pkg(module+"/vendors/nxos", nil, []string{"github.com/spf13/cobra"}),
		pkg(module+"/cli", nil, nil),
	)
	want := []string{
		"no cobra below cli: ./vendors/nxos -> github.com/spf13/cobra",
		"parser stays vendor neutral: ./parser -> ./vendors/nxos",
		"parser stays vendor neutral: ./parser -> os",
		"review uses only parser: ./review -> ./vendors",
		"undeclared package: ./cli",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got:\n%s", strings.Join(got, "\n"))
	}
}

func TestDecodeConfigRejectsMalformedRules(t *testing.T) {
	for _, doc := range []string{
		"rules: [{name: x, packages: [./a]}]",
		"rules: [{packages: [./a], deny: [os]}]",
		"rules: [{name: x, packages: [parser], deny: [os]}]",
		"rule: []",
	} {
		if _, err := archcheck.DecodeConfig([]byte(doc)); err == nil {
			t.Fatalf("accepted %q", doc)
		}
	}
}

func TestDecodePackages(t *testing.T) {
	ps, err := archcheck.DecodePackages(strings.NewReader(`{"ImportPath":"a","Imports":["b"]}{"ImportPath":"b"}`))
	if err != nil || len(ps) != 2 || ps[0].Imports[0] != "b" {
		t.Fatal(ps, err)
	}
	if _, err := archcheck.DecodePackages(strings.NewReader(`{"ImportPath":`)); err == nil {
		t.Fatal("accepted truncated output")
	}
}
