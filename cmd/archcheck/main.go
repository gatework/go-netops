// Command archcheck enforces a module's declared package import boundaries.
//
// Usage, from the module root:
//
//	go tool archcheck [-rules architecture.yaml] [packages]
//
// Add it to a consumer module once with
//
//	go get -tool github.com/gatework/go-netops/cmd/archcheck@<version>
//
// It runs `go list -json` on the packages (default ./...) and exits 1 with
// one line per violation.
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"

	"github.com/gatework/go-netops/archcheck"
)

func main() {
	os.Exit(run())
}

func run() int {
	rules := flag.String("rules", "architecture.yaml", "rule table")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	body, err := os.ReadFile(*rules)
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck:", err)
		return 2
	}
	config, err := archcheck.DecodeConfig(body)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	patterns := flag.Args()
	if len(patterns) == 0 {
		patterns = []string{"./..."}
	}
	module, err := exec.CommandContext(ctx, "go", "list", "-m").Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: go list -m:", err)
		return 2
	}
	out, err := exec.CommandContext(ctx, "go", append([]string{"list", "-json=ImportPath,Imports,Deps"}, patterns...)...).Output()
	if err != nil {
		fmt.Fprintln(os.Stderr, "archcheck: go list:", err)
		return 2
	}
	packages, err := archcheck.DecodePackages(bytes.NewReader(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	violations := archcheck.Check(config, string(bytes.TrimSpace(module)), packages)
	if len(violations) > 0 {
		fmt.Fprintln(os.Stderr, "architecture violations:")
		for _, v := range violations {
			fmt.Fprintln(os.Stderr, " ", v)
		}
		return 1
	}
	fmt.Printf("PASS: %d packages satisfy %d rules\n", len(packages), len(config.Rules))
	return 0
}
