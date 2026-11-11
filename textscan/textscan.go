// Package textscan holds the mechanics shared by command-output parsers:
// compiled-once patterns, line scanning with issue accounting, and label
// tables that map vendor labels to canonical field names.
//
// It knows no vendor syntax. Vendor packages declare their patterns and
// labels as package-level values and consume these helpers.
package textscan

import (
	"iter"
	"regexp"
	"strings"
	"sync"

	"github.com/gatework/go-netops/evidence"
)

var patterns sync.Map

// Pattern returns the compiled form of expr, compiling it once per process.
// Prefer package-level regexp.MustCompile for fixed patterns; Pattern is for
// patterns assembled from declarations at run time. It panics on an invalid
// expression, which is a programming error in the declaration.
func Pattern(expr string) *regexp.Regexp {
	if v, ok := patterns.Load(expr); ok {
		if re, ok := v.(*regexp.Regexp); ok {
			return re
		}
	}
	v, _ := patterns.LoadOrStore(expr, regexp.MustCompile(expr))
	re, _ := v.(*regexp.Regexp) // the cache stores only *regexp.Regexp
	return re
}

// Line is one line of output with its 1-based number.
type Line struct {
	Number int
	Text   string
}

// Lines iterates the lines of text, trimming a trailing carriage return.
func Lines(text string) iter.Seq[Line] {
	return func(yield func(Line) bool) {
		n := 0
		for raw := range strings.SplitSeq(text, "\n") {
			n++
			if !yield(Line{Number: n, Text: strings.TrimSuffix(raw, "\r")}) {
				return
			}
		}
	}
}

// Scanner accumulates issues while a parser walks output lines. It replaces
// the per-parser "append unparsed_row with line number" boilerplate.
type Scanner struct {
	issues []evidence.Issue
}

// Unparsed records a line that matched no row or header pattern.
func (s *Scanner) Unparsed(line int) { s.Issue("unparsed_row", line) }

// Issue records a coded issue at a line.
func (s *Scanner) Issue(code string, line int) {
	s.issues = append(s.issues, evidence.Issue{Code: code, Line: line})
}

// Issues returns the recorded issues.
func (s *Scanner) Issues() []evidence.Issue { return append([]evidence.Issue(nil), s.issues...) }

// Status derives the observation status: complete only when the response was
// complete and every line was understood.
func (s *Scanner) Status(responseComplete bool) evidence.Status {
	if responseComplete && len(s.issues) == 0 {
		return evidence.StatusComplete
	}
	return evidence.StatusPartial
}

// Labels maps canonical field names to the vendor labels that mean them.
// Labels are matched case-insensitively with whitespace collapsed, so
// "Client  Count" and "client count" are the same label.
type Labels map[string][]string

// LabelTable is the compiled, reverse index of a Labels declaration.
type LabelTable struct{ index map[string]string }

// Compile builds the reverse index and reports a label claimed by two fields,
// which would make parsing order-dependent.
func (l Labels) Compile() (*LabelTable, error) {
	t := &LabelTable{index: map[string]string{}}
	for field, labels := range l {
		for _, label := range labels {
			key := NormalizeLabel(label)
			if prev, dup := t.index[key]; dup && prev != field {
				return nil, &DuplicateLabelError{Label: label, Fields: [2]string{prev, field}}
			}
			t.index[key] = field
		}
	}
	return t, nil
}

// MustCompile is Compile for package-level declarations.
func (l Labels) MustCompile() *LabelTable {
	t, err := l.Compile()
	if err != nil {
		panic(err)
	}
	return t
}

// Field returns the canonical field for a vendor label.
func (t *LabelTable) Field(label string) (string, bool) {
	f, ok := t.index[NormalizeLabel(label)]
	return f, ok
}

// DuplicateLabelError reports one label mapped to two fields.
type DuplicateLabelError struct {
	Label  string
	Fields [2]string
}

func (e *DuplicateLabelError) Error() string {
	return "textscan: label " + e.Label + " maps to both " + e.Fields[0] + " and " + e.Fields[1]
}

// NormalizeLabel lowercases and collapses whitespace.
func NormalizeLabel(label string) string {
	return strings.Join(strings.Fields(strings.ToLower(label)), " ")
}

// SplitLabel splits "Label : value" at the first separator. ok is false when
// the line has no separator or an empty label.
func SplitLabel(line, separator string) (label, value string, ok bool) {
	label, value, ok = strings.Cut(line, separator)
	label, value = strings.TrimSpace(label), strings.TrimSpace(value)
	return label, value, ok && label != ""
}
