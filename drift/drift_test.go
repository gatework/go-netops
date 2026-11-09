package drift_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/gatework/go-netops/assess"
	"github.com/gatework/go-netops/drift"
)

func member(host string, mtu int, present bool) drift.Member[int] {
	return drift.Member[int]{Subject: assess.NewSubject("device", host), Value: mtu, Present: present}
}

func TestConsensusIdentifiesMinority(t *testing.T) {
	expected, ok, devs := drift.Consensus("svi.mtu", []drift.Member[int]{
		member("a", 9216, true), member("b", 9216, true), member("c", 1500, true), member("d", 0, false),
	})
	if !ok || expected != 9216 || len(devs) != 2 {
		t.Fatalf("expected=%v ok=%v devs=%v", expected, ok, devs)
	}
	if devs[0].Subject.Key != "c" || devs[0].Kind != drift.KindMismatch || devs[0].Actual != 1500 || devs[0].Expected != 9216 {
		t.Fatalf("minority not reported: %+v", devs[0])
	}
	if devs[1].Subject.Key != "d" || devs[1].Kind != drift.KindMissing {
		t.Fatalf("absent member not reported: %+v", devs[1])
	}
}

func TestConsensusWithoutMajorityIsAmbiguous(t *testing.T) {
	_, ok, devs := drift.Consensus("svi.mtu", []drift.Member[int]{member("a", 9216, true), member("b", 1500, true)})
	if ok || len(devs) != 2 || devs[0].Kind != drift.KindAmbiguous {
		t.Fatalf("tie should be ambiguous: %v", devs)
	}
}

func TestConsensusAbsenceAsMajority(t *testing.T) {
	_, ok, devs := drift.Consensus("svi.acl", []drift.Member[int]{member("a", 0, false), member("b", 0, false), member("c", 7, true)})
	if ok || len(devs) != 1 || devs[0].Kind != drift.KindUnexpected || devs[0].Subject.Key != "c" {
		t.Fatalf("absence majority: %v", devs)
	}
}

func TestConsensusAgreement(t *testing.T) {
	expected, ok, devs := drift.Consensus("svi.mtu", []drift.Member[int]{member("a", 9216, true), member("b", 9216, true)})
	if !ok || expected != 9216 || devs != nil {
		t.Fatalf("agreement reported deviations: %v", devs)
	}
	if _, ok, devs := drift.Consensus[int]("x", nil); ok || devs != nil {
		t.Fatal("empty membership produced a consensus")
	}
}

func TestConsensusByListValues(t *testing.T) {
	m := func(host string, rts ...string) drift.Member[[]string] {
		return drift.Member[[]string]{Subject: assess.NewSubject("device", host), Value: rts, Present: true}
	}
	key := func(v []string) string { s := slices.Sorted(slices.Values(v)); return strings.Join(s, ",") }
	_, ok, devs := drift.ConsensusBy("vni.route_targets", []drift.Member[[]string]{m("a", "1:1", "2:2"), m("b", "2:2", "1:1"), m("c", "1:1")}, key)
	if !ok || len(devs) != 1 || devs[0].Subject.Key != "c" {
		t.Fatalf("order-insensitive consensus failed: %v", devs)
	}
}

func TestExpectAndSets(t *testing.T) {
	s := assess.NewSubject("ssid", "corp")
	if _, drifted := drift.Expect(s, "vlan", 10, 10, true); drifted {
		t.Fatal("equal value drifted")
	}
	if d, drifted := drift.Expect(s, "vlan", 10, 20, true); !drifted || d.Kind != drift.KindMismatch || d.Source != drift.SourceBaseline {
		t.Fatalf("mismatch not reported: %+v", d)
	}
	if d, drifted := drift.Expect(s, "vlan", 10, 0, false); !drifted || d.Kind != drift.KindMissing {
		t.Fatalf("missing not reported: %+v", d)
	}
	devs := drift.Sets(s, "vlans", []int{10, 20}, []int{20, 30})
	if len(devs) != 2 || devs[0].Kind != drift.KindMissing || devs[0].Expected != 10 || devs[1].Kind != drift.KindUnexpected || devs[1].Actual != 30 {
		t.Fatalf("set drift: %v", devs)
	}
}

func TestChanges(t *testing.T) {
	subject := func(ap string) assess.Subject { return assess.NewSubject("ap", ap) }
	devs := drift.Changes(subject, "uplink", map[string]string{"ap1": "sw1/1", "ap2": "sw1/2"}, map[string]string{"ap1": "sw2/1", "ap3": "sw1/3"})
	kinds := []drift.Kind{}
	for _, d := range devs {
		kinds = append(kinds, d.Kind)
	}
	if !slices.Equal(kinds, []drift.Kind{drift.KindChanged, drift.KindMissing, drift.KindUnexpected}) {
		t.Fatalf("changes: %v", devs)
	}
	if f := devs[0].Finding("uplink moved"); f.Category != assess.CategoryDrift || f.Expected != "sw1/1" || f.Actual != "sw2/1" {
		t.Fatalf("finding conversion: %+v", f)
	}
}
