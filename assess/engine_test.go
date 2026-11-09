package assess_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gatework/go-netops/assess"
	"github.com/gatework/go-netops/evidence"
)

type model struct{ peersDown []string }

const peers evidence.Dimension = "nve_peers"

func peerRule() assess.Rule[model] {
	return assess.NewRule(assess.RuleMeta{
		ID: "vxlan.nve_peer_down", Title: "NVE peer down", Severity: assess.SeverityHigh,
		Category: assess.CategoryRuntime, Requires: []evidence.Dimension{peers}, Verify: []evidence.Dimension{peers},
		Params: assess.Params{"tolerated": 0},
	}, func(in assess.Input[model]) []assess.Finding {
		if len(in.Model.peersDown) <= in.Params.Int("tolerated", 0) {
			return nil
		}
		var out []assess.Finding
		for _, p := range in.Model.peersDown {
			out = append(out, assess.Finding{Subject: assess.NewSubject("peer", p)})
		}
		return out
	})
}

func infoRule() assess.Rule[model] {
	return assess.NewRule(assess.RuleMeta{ID: "vxlan.inventory", Title: "inventory", Severity: assess.SeverityInfo, Category: assess.CategoryEvidence},
		func(assess.Input[model]) []assess.Finding {
			return []assess.Finding{{Subject: assess.NewSubject("fabric", "dc1")}}
		})
}

type commands map[evidence.Dimension]string

func (c commands) Command(d evidence.Dimension) (string, error) {
	if v, ok := c[d]; ok {
		return v, nil
	}
	return "", errors.New("unknown")
}

func outcome(a assess.Assessment, id assess.RuleID) assess.RuleOutcome {
	for _, o := range a.Rules {
		if o.Rule == id {
			return o
		}
	}
	return assess.RuleOutcome{}
}

func TestEngineRunsRulesAndOrdersByRisk(t *testing.T) {
	catalog, err := assess.NewCatalog(infoRule(), peerRule())
	if err != nil {
		t.Fatal(err)
	}
	engine, err := assess.NewEngine(catalog, assess.Policy{}, commands{peers: "show nve peers"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := engine.Run(t.Context(), model{peersDown: []string{"10.0.0.2", "10.0.0.1"}}, evidence.Coverage{peers: evidence.StatusComplete})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Findings) != 3 || a.Findings[0].Severity != assess.SeverityHigh || a.Findings[0].Subject.Key != "10.0.0.1" {
		t.Fatalf("findings not ordered by severity then subject: %+v", a.Findings)
	}
	f := a.Findings[0]
	if f.Rule != "vxlan.nve_peer_down" || f.Title != "NVE peer down" || f.Category != assess.CategoryRuntime || len(f.Verify) != 1 || f.Verify[0] != "show nve peers" {
		t.Fatalf("engine did not fill metadata: %+v", f)
	}
	if a.Counts[assess.SeverityHigh] != 2 || a.Counts[assess.SeverityInfo] != 1 || a.Highest() != assess.SeverityHigh || a.Incomplete() {
		t.Fatalf("counts %v highest %v", a.Counts, a.Highest())
	}
	body, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"counts":{"high":2,"info":1}`) || !strings.Contains(string(body), `"severity":"high"`) {
		t.Fatalf("unexpected JSON: %s", body)
	}
}

func TestEngineSkipsRuleWithIncompleteEvidence(t *testing.T) {
	catalog, _ := assess.NewCatalog(peerRule())
	engine, _ := assess.NewEngine(catalog, assess.Policy{}, nil)
	for _, status := range []evidence.Status{evidence.StatusPartial, evidence.StatusFailed, evidence.StatusNotRequested} {
		a, err := engine.Run(t.Context(), model{peersDown: []string{"x"}}, evidence.Coverage{peers: status})
		if err != nil {
			t.Fatal(err)
		}
		o := outcome(a, "vxlan.nve_peer_down")
		if len(a.Findings) != 0 || o.State != assess.RuleSkipped || o.Reason != "insufficient_evidence" || len(o.Missing) != 1 || !a.Incomplete() {
			t.Fatalf("%s: rule concluded on incomplete evidence: %+v", status, a)
		}
	}
}

func TestEnginePolicyOverridesAndDisables(t *testing.T) {
	catalog, _ := assess.NewCatalog(infoRule(), peerRule())
	policy, err := assess.DecodePolicy([]byte(`
rules:
  vxlan.inventory:
    enabled: false
  vxlan.nve_peer_down:
    severity: critical
    params:
      tolerated: 1
`))
	if err != nil {
		t.Fatal(err)
	}
	engine, err := assess.NewEngine(catalog, policy, nil)
	if err != nil {
		t.Fatal(err)
	}
	cov := evidence.Coverage{peers: evidence.StatusComplete}
	a, _ := engine.Run(t.Context(), model{peersDown: []string{"a"}}, cov)
	if len(a.Findings) != 0 || outcome(a, "vxlan.inventory").State != assess.RuleDisabled {
		t.Fatalf("threshold or disable ignored: %+v", a)
	}
	a, _ = engine.Run(t.Context(), model{peersDown: []string{"a", "b"}}, cov)
	if len(a.Findings) != 2 || a.Findings[0].Severity != assess.SeverityCritical {
		t.Fatalf("severity override ignored: %+v", a.Findings)
	}
}

func TestPolicyValidation(t *testing.T) {
	catalog, _ := assess.NewCatalog(peerRule())
	for name, doc := range map[string]string{
		"unknown rule":  "rules: {nope: {enabled: true}}",
		"unknown param": "rules: {vxlan.nve_peer_down: {params: {nope: 1}}}",
		"param type":    "rules: {vxlan.nve_peer_down: {params: {tolerated: many}}}",
	} {
		p, err := assess.DecodePolicy([]byte(doc))
		if err != nil {
			t.Fatalf("%s: decode: %v", name, err)
		}
		if _, err := assess.NewEngine(catalog, p, nil); !errors.Is(err, assess.ErrInvalid) {
			t.Fatalf("%s: accepted invalid policy", name)
		}
	}
	for name, doc := range map[string]string{"unknown field": "rule: {}", "severity": "rules: {x: {severity: severe}}"} {
		if _, err := assess.DecodePolicy([]byte(doc)); err == nil {
			t.Fatalf("%s: decoded invalid policy", name)
		}
	}
}

func TestEngineIsolatesPanickingRule(t *testing.T) {
	boom := assess.NewRule(assess.RuleMeta{ID: "x.boom", Title: "boom", Severity: assess.SeverityLow, Category: assess.CategoryRuntime},
		func(assess.Input[model]) []assess.Finding { panic("bug") })
	catalog, _ := assess.NewCatalog(boom, infoRule())
	engine, _ := assess.NewEngine(catalog, assess.Policy{}, nil)
	a, err := engine.Run(t.Context(), model{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if outcome(a, "x.boom").State != assess.RuleFailed || len(a.Findings) != 1 || !a.Incomplete() {
		t.Fatalf("panic not isolated: %+v", a)
	}
}

func TestEngineStopsOnCancellation(t *testing.T) {
	catalog, _ := assess.NewCatalog(infoRule())
	engine, _ := assess.NewEngine(catalog, assess.Policy{}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a, err := engine.Run(ctx, model{}, nil)
	if !errors.Is(err, context.Canceled) || len(a.Findings) != 0 || outcome(a, "vxlan.inventory").Reason != "cancelled" {
		t.Fatalf("cancellation ignored: %+v %v", a, err)
	}
}

func TestCatalogRejectsInvalidRules(t *testing.T) {
	bad := []assess.RuleMeta{
		{ID: "Bad", Title: "t", Severity: assess.SeverityLow, Category: assess.CategoryRuntime},
		{ID: "a.b", Severity: assess.SeverityLow, Category: assess.CategoryRuntime},
		{ID: "a.b", Title: "t", Category: assess.CategoryRuntime},
		{ID: "a.b", Title: "t", Severity: assess.SeverityLow, Category: "misc"},
	}
	for _, meta := range bad {
		r := assess.NewRule(meta, func(assess.Input[model]) []assess.Finding { return nil })
		if _, err := assess.NewCatalog(r); !errors.Is(err, assess.ErrInvalid) {
			t.Fatalf("accepted %+v", meta)
		}
	}
	if _, err := assess.NewCatalog(infoRule(), infoRule()); !errors.Is(err, assess.ErrInvalid) {
		t.Fatal("accepted duplicate rule id")
	}
}

func TestSubjectString(t *testing.T) {
	s := assess.NewSubject("fabric", "dc1").Child("vni", "10010").Child("device", "10.0.0.1")
	if got := s.String(); got != "fabric:dc1/vni:10010/device:10.0.0.1" {
		t.Fatal(got)
	}
}
