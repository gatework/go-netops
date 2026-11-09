package evidence_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/gatework/go-netops/evidence"
)

type peer struct {
	Address string `json:"address"`
	State   string `json:"state"`
	Uptime  int64  `json:"uptime"`
}

func TestFingerprintIsRepresentationIndependent(t *testing.T) {
	a, err := evidence.Fingerprint(peer{Address: "10.0.0.1", State: "Up", Uptime: 9007199254740993})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := evidence.Fingerprint(map[string]any{"uptime": json.Number("9007199254740993"), "state": "Up", "address": "10.0.0.1"})
	if a != b || !evidence.ValidDigest(a) {
		t.Fatalf("%s != %s", a, b)
	}
	c, _ := evidence.Canonical(map[string]string{"z": "<&>", "a": "\u2028"})
	if string(c) != `{"a":"\u2028","z":"<&>"}` {
		t.Fatalf("canonical form: %q", c)
	}
	if _, err := evidence.Fingerprint(func() {}); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatal("unencodable value accepted")
	}
}

func TestObservation(t *testing.T) {
	o, err := evidence.NewObservation[peer]("nve_peers", evidence.StatusComplete, nil, nil, nil)
	if err != nil || !o.Complete() || o.Facts == nil {
		t.Fatalf("%+v %v", o, err)
	}
	body, _ := json.Marshal(o)
	if string(body) != `{"dimension":"nve_peers","status":"complete","facts":[],"issues":[],"sources":[]}` {
		t.Fatalf("%s", body)
	}
	if _, err := evidence.NewObservation[peer]("", evidence.StatusComplete, nil, nil, nil); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatal("empty dimension")
	}
	if _, err := evidence.NewObservation[peer]("x", "done", nil, nil, nil); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatal("invalid status")
	}
	if _, err := evidence.NewObservation[peer]("x", evidence.StatusPartial, nil, []evidence.Issue{{}}, nil); !errors.Is(err, evidence.ErrInvalid) {
		t.Fatal("issue without code")
	}
	var s evidence.Status
	if err := json.Unmarshal([]byte(`"bogus"`), &s); err == nil {
		t.Fatal("decoded invalid status")
	}
}

func TestCoverage(t *testing.T) {
	c := evidence.Coverage{"a": evidence.StatusComplete, "b": evidence.StatusPartial, "c": evidence.StatusNotRequested}
	if c.Complete() || len(c.Missing("a", "b", "z")) != 2 || c.Status("z") != evidence.StatusNotRequested {
		t.Fatal("coverage")
	}
	if (evidence.Coverage{"c": evidence.StatusNotRequested}).Complete() {
		t.Fatal("nothing requested is not complete")
	}
	if !(evidence.Coverage{"a": evidence.StatusComplete, "c": evidence.StatusNotRequested}).Complete() {
		t.Fatal("requested dimensions complete")
	}
}

func FuzzCanonicalIsStable(f *testing.F) {
	f.Add(`{"b":[1,2.50,{"x":null}],"a":"<"}`)
	f.Fuzz(func(t *testing.T, doc string) {
		var v any
		d := json.NewDecoder(stringsReader(doc))
		d.UseNumber()
		if d.Decode(&v) != nil {
			return
		}
		once, err := evidence.Canonical(v)
		if err != nil {
			return
		}
		var again any
		d = json.NewDecoder(stringsReader(string(once)))
		d.UseNumber()
		if err := d.Decode(&again); err != nil {
			t.Fatal(err)
		}
		twice, _ := evidence.Canonical(again)
		if string(once) != string(twice) {
			t.Fatalf("not idempotent: %s vs %s", once, twice)
		}
	})
}
