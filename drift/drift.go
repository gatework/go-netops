// Package drift detects deviations of observed state from what is expected.
//
// Expectations come from three sources, all reported as the same Deviation:
//
//   - Baseline: an operator-declared intent ("anycast gateway MAC is X").
//   - Consensus: peers in the same role should agree; the majority value is
//     expected and the minority members deviate.
//   - Previous: the last snapshot; values that appeared, disappeared or changed.
package drift

import (
	"cmp"
	"fmt"
	"maps"
	"slices"

	"github.com/gatework/go-netops/assess"
)

// Kind classifies a deviation.
type Kind string

const (
	// KindMissing means expected but absent.
	KindMissing Kind = "missing"
	// KindUnexpected means present but not expected.
	KindUnexpected Kind = "unexpected"
	// KindMismatch means present with a different value than expected.
	KindMismatch Kind = "mismatch"
	// KindAmbiguous means peers disagree with no majority to identify the outlier.
	KindAmbiguous Kind = "ambiguous"
	// KindChanged means it differs from the previous snapshot.
	KindChanged Kind = "changed"
)

// Source names where the expectation came from.
type Source string

// Expectation sources.
const (
	SourceBaseline  Source = "baseline"
	SourceConsensus Source = "consensus"
	SourcePrevious  Source = "previous"
)

// Deviation is one observed difference from an expectation. Path names the
// attribute within the subject, such as "svi.mtu" or "ssid.vlan".
type Deviation struct {
	Subject  assess.Subject `json:"subject"`
	Path     string         `json:"path"`
	Kind     Kind           `json:"kind"`
	Source   Source         `json:"source"`
	Expected any            `json:"expected,omitempty"`
	Actual   any            `json:"actual,omitempty"`
}

// String renders a compact, log-safe description.
func (d Deviation) String() string {
	return fmt.Sprintf("%s %s %s (%s): expected %v, actual %v", d.Subject, d.Path, d.Kind, d.Source, d.Expected, d.Actual)
}

// Finding converts the deviation into a drift finding. The engine fills rule
// identity and severity from the rule that reports it.
func (d Deviation) Finding(detail string) assess.Finding {
	return assess.Finding{
		Category: assess.CategoryDrift,
		Subject:  d.Subject,
		Detail:   detail,
		Expected: d.Expected,
		Actual:   d.Actual,
	}
}

// Member is one peer's observed value for a consensus comparison. A member
// with Present false lacks the attribute entirely.
type Member[V any] struct {
	Subject assess.Subject
	Value   V
	Present bool
}

// Consensus compares peers that should agree. Each member votes for its
// value, or for "absent" when it lacks the attribute. The unique value with
// the most votes, held by at least two members, is the expectation; members
// that differ deviate (mismatch, missing, or unexpected when the expectation
// is absence). A tie for the most votes cannot identify the outlier, so every
// member is reported as ambiguous. Agreement, or fewer than two members,
// yields no deviations.
func Consensus[V comparable](path string, members []Member[V]) (expected V, ok bool, devs []Deviation) {
	return ConsensusBy(path, members, func(v V) V { return v })
}

type vote[K comparable] struct {
	present bool
	key     K
}

// ConsensusBy is Consensus for values that are not comparable; key maps a
// value to a comparable identity such as a sorted, joined list.
func ConsensusBy[V any, K comparable](path string, members []Member[V], key func(V) K) (expected V, ok bool, devs []Deviation) {
	if len(members) < 2 {
		if len(members) == 1 && members[0].Present {
			return members[0].Value, true, nil
		}
		return expected, false, nil
	}
	voteOf := func(m Member[V]) vote[K] {
		if !m.Present {
			return vote[K]{}
		}
		return vote[K]{present: true, key: key(m.Value)}
	}
	counts := map[vote[K]]int{}
	value := map[vote[K]]V{}
	for _, m := range members {
		v := voteOf(m)
		if _, seen := value[v]; !seen {
			value[v] = m.Value
		}
		counts[v]++
	}
	if len(counts) == 1 {
		for v := range counts {
			return value[v], v.present, nil
		}
	}
	var winner vote[K]
	best, tied := 0, false
	for v, n := range counts {
		switch {
		case n > best:
			winner, best, tied = v, n, false
		case n == best:
			tied = true
		}
	}
	if tied || best < 2 {
		for _, m := range members {
			d := Deviation{Subject: m.Subject, Path: path, Kind: KindAmbiguous, Source: SourceConsensus}
			if m.Present {
				d.Actual = m.Value
			}
			devs = append(devs, d)
		}
		return expected, false, devs
	}
	if winner.present {
		expected = value[winner]
	}
	for _, m := range members {
		v := voteOf(m)
		switch {
		case v == winner:
		case !winner.present:
			devs = append(devs, Deviation{Subject: m.Subject, Path: path, Kind: KindUnexpected, Source: SourceConsensus, Actual: m.Value})
		case !m.Present:
			devs = append(devs, Deviation{Subject: m.Subject, Path: path, Kind: KindMissing, Source: SourceConsensus, Expected: expected})
		default:
			devs = append(devs, Deviation{Subject: m.Subject, Path: path, Kind: KindMismatch, Source: SourceConsensus, Expected: expected, Actual: m.Value})
		}
	}
	return expected, winner.present, devs
}

// Expect compares one actual value against a declared baseline value.
func Expect[V comparable](subject assess.Subject, path string, expected, actual V, present bool) (Deviation, bool) {
	switch {
	case !present:
		return Deviation{Subject: subject, Path: path, Kind: KindMissing, Source: SourceBaseline, Expected: expected}, true
	case actual != expected:
		return Deviation{Subject: subject, Path: path, Kind: KindMismatch, Source: SourceBaseline, Expected: expected, Actual: actual}, true
	}
	return Deviation{}, false
}

// Sets compares a declared set of members against the observed set. Members
// are reported in sorted order: missing first, then unexpected.
func Sets[K cmp.Ordered](subject assess.Subject, path string, expected, actual []K) []Deviation {
	want, have := setOf(expected), setOf(actual)
	var devs []Deviation
	for _, k := range slices.Sorted(maps.Keys(want)) {
		if !have[k] {
			devs = append(devs, Deviation{Subject: subject, Path: path, Kind: KindMissing, Source: SourceBaseline, Expected: k})
		}
	}
	for _, k := range slices.Sorted(maps.Keys(have)) {
		if !want[k] {
			devs = append(devs, Deviation{Subject: subject, Path: path, Kind: KindUnexpected, Source: SourceBaseline, Actual: k})
		}
	}
	return devs
}

// Changes compares two keyed snapshots of the same attribute. Keys only in
// previous are missing, keys only in current are unexpected, and keys whose
// value differs are changed. Results are in key order.
func Changes[K cmp.Ordered, V comparable](subject func(K) assess.Subject, path string, previous, current map[K]V) []Deviation {
	keys := slices.Sorted(maps.Keys(previous))
	for k := range current {
		if _, ok := previous[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var devs []Deviation
	for _, k := range keys {
		before, wasThere := previous[k]
		after, isThere := current[k]
		switch {
		case wasThere && !isThere:
			devs = append(devs, Deviation{Subject: subject(k), Path: path, Kind: KindMissing, Source: SourcePrevious, Expected: before})
		case !wasThere && isThere:
			devs = append(devs, Deviation{Subject: subject(k), Path: path, Kind: KindUnexpected, Source: SourcePrevious, Actual: after})
		case before != after:
			devs = append(devs, Deviation{Subject: subject(k), Path: path, Kind: KindChanged, Source: SourcePrevious, Expected: before, Actual: after})
		}
	}
	return devs
}

func setOf[K comparable](xs []K) map[K]bool {
	m := make(map[K]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}
