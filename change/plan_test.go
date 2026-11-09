package change_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/gatework/go-netops/change"
	"github.com/gatework/go-netops/evidence"
)

type config struct{ vlans map[int]bool }

type addVLAN struct {
	VLAN int `json:"vlan"`
}

func (addVLAN) Kind() change.Kind { return "vlan_add" }
func (a addVLAN) Validate() error {
	if a.VLAN < 1 || a.VLAN > 4094 {
		return errors.New("vlan out of range")
	}
	return nil
}
func (a addVLAN) Render() (change.Script, error) {
	return change.Script{Commands: []string{"configure terminal", fmt.Sprintf("vlan %d", a.VLAN), "end"}, Rollback: []string{"configure terminal", fmt.Sprintf("no vlan %d", a.VLAN), "end"}}, nil
}
func (a addVLAN) Eligible(c config) error {
	if c.vlans[a.VLAN] {
		return errors.New("already present")
	}
	return nil
}
func (a addVLAN) Satisfied(c config) error {
	if !c.vlans[a.VLAN] {
		return errors.New("absent")
	}
	return nil
}

var target = change.Target{Host: "10.0.0.1", Platform: "cisco_nxos"}
var digest = evidence.Digest([]byte("config"))

func decode(kind change.Kind, body json.RawMessage) (change.Change[config], error) {
	if kind != "vlan_add" {
		return nil, errors.New("kind not allowed")
	}
	var a addVLAN
	return a, json.Unmarshal(body, &a)
}

func TestPlanChecks(t *testing.T) {
	p, err := change.NewPlan[config](target, digest, addVLAN{VLAN: 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckBefore(config{}, "other"); !errors.Is(err, change.ErrDrifted) {
		t.Fatalf("drift not detected: %v", err)
	}
	if err := p.CheckBefore(config{vlans: map[int]bool{10: true}}, digest); !errors.Is(err, change.ErrNotEligible) {
		t.Fatalf("eligibility not checked: %v", err)
	}
	if err := p.CheckBefore(config{}, digest); err != nil {
		t.Fatal(err)
	}
	if err := p.CheckAfter(config{}); !errors.Is(err, change.ErrNotSatisfied) {
		t.Fatalf("readback not checked: %v", err)
	}
	cmds := p.Commands()
	cmds[0] = "reload"
	if p.Commands()[0] != "configure terminal" {
		t.Fatal("plan commands are mutable")
	}
}

func TestNewPlanRejectsInvalid(t *testing.T) {
	cases := map[string]func() error{
		"host": func() error {
			_, e := change.NewPlan[config](change.Target{Host: "010.0.0.1", Platform: "x"}, digest, addVLAN{VLAN: 1})
			return e
		},
		"platform": func() error {
			_, e := change.NewPlan[config](change.Target{Host: "10.0.0.1"}, digest, addVLAN{VLAN: 1})
			return e
		},
		"digest": func() error { _, e := change.NewPlan[config](target, "abc", addVLAN{VLAN: 1}); return e },
		"change": func() error { _, e := change.NewPlan[config](target, digest, addVLAN{VLAN: 5000}); return e },
		"nil":    func() error { _, e := change.NewPlan[config](target, digest, nil); return e },
	}
	for name, f := range cases {
		if err := f(); !errors.Is(err, change.ErrInvalidPlan) {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestEnvelopeRoundTripRejectsEditedCommands(t *testing.T) {
	p, _ := change.NewPlan[config](target, digest, addVLAN{VLAN: 10})
	body, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	back, err := change.DecodePlan(body, decode)
	if err != nil {
		t.Fatal(err)
	}
	if back.Kind() != "vlan_add" || back.ExpectedFingerprint() != digest {
		t.Fatalf("round trip lost identity: %v", back)
	}
	edited := strings.Replace(string(body), `"vlan 10"`, `"vlan 10 ; reload"`, 1)
	if _, err := change.DecodePlan([]byte(edited), decode); !errors.Is(err, change.ErrInvalidPlan) {
		t.Fatalf("edited commands accepted: %v", err)
	}
	unknown := strings.Replace(string(body), `"kind":"vlan_add"`, `"kind":"vlan_delete"`, 1)
	if _, err := change.DecodePlan([]byte(unknown), decode); !errors.Is(err, change.ErrInvalidPlan) {
		t.Fatalf("disallowed kind accepted: %v", err)
	}
	extra := strings.Replace(string(body), `{"schema_version"`, `{"x":1,"schema_version"`, 1)
	if _, err := change.DecodePlan([]byte(extra), decode); !errors.Is(err, change.ErrInvalidPlan) {
		t.Fatal("unknown field accepted")
	}
}
