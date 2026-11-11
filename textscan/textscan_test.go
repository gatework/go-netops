package textscan_test

import (
	"errors"
	"testing"

	"github.com/gatework/go-netops/evidence"
	"github.com/gatework/go-netops/textscan"
)

func TestLabelTable(t *testing.T) {
	table := textscan.Labels{
		"client_count": {"Client Count", "Number of clients", "online clients"},
		"ssid":         {"SSID"},
	}.MustCompile()
	if f, ok := table.Field("  number   OF Clients "); !ok || f != "client_count" {
		t.Fatal(f, ok)
	}
	if _, ok := table.Field("bssid"); ok {
		t.Fatal("unknown label matched")
	}
	_, err := textscan.Labels{"a": {"x"}, "b": {"X"}}.Compile()
	if dup, ok := errors.AsType[*textscan.DuplicateLabelError](err); !ok || dup.Label == "" {
		t.Fatalf("duplicate label accepted: %v", err)
	}
}

func TestScanner(t *testing.T) {
	row := textscan.Pattern(`^(\S+)\s+(Up|Down)$`)
	var s textscan.Scanner
	var up []string
	for line := range textscan.Lines("10.0.0.1 Up\r\ngarbage\n10.0.0.2 Down") {
		m := row.FindStringSubmatch(line.Text)
		if m == nil {
			s.Unparsed(line.Number)
			continue
		}
		up = append(up, m[1])
	}
	if len(up) != 2 || len(s.Issues()) != 1 || s.Issues()[0].Line != 2 || s.Status(true) != evidence.StatusPartial {
		t.Fatalf("%v %v", up, s.Issues())
	}
	if first, second := textscan.Pattern(`^x$`), textscan.Pattern(`^x$`); first != second {
		t.Fatal("pattern not cached")
	}
	var clean textscan.Scanner
	if clean.Status(true) != evidence.StatusComplete || clean.Status(false) != evidence.StatusPartial {
		t.Fatal("status")
	}
	if l, v, ok := textscan.SplitLabel(" AP name : ap-1 ", ":"); !ok || l != "AP name" || v != "ap-1" {
		t.Fatal(l, v)
	}
}
