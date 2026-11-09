package inventory_test

import (
	"errors"
	"testing"
	"time"

	ncinventory "github.com/gatework/go-net-connector/inventory"
	"github.com/gatework/go-net-connector/netdisco"
	"github.com/gatework/go-net-connector/vendors"
	"github.com/gatework/go-netops/inventory"
)

func TestDecodeAppliesDefaults(t *testing.T) {
	c, err := inventory.DecodeYAML([]byte("netdisco:\n  url: https://nd.example\n"))
	if err != nil {
		t.Fatal(err)
	}
	l, _ := c.Limits()
	if l.PageSize != 500 || l.Timeout != 300*time.Second {
		t.Fatalf("%+v", l)
	}
	o := c.FetchOptions()
	if o.Mode != netdisco.CachePrefer || o.Cache == nil || *o.Cache.TTL != 72*time.Hour {
		t.Fatalf("%+v", o)
	}
	empty, err := inventory.DecodeYAML(nil)
	if err != nil || empty.Netdisco.Source != "" {
		t.Fatal(err)
	}
}

func TestDecodeFromMergedLayers(t *testing.T) {
	c, err := inventory.Decode(map[string]any{
		"netdisco":  map[string]any{"source": "postgres", "query": "select 1", "db_port": 5433, "cache_enabled": false},
		"inventory": map[string]any{"include_vendors": []any{"cisco"}, "device_rules": []any{map[string]any{"vendor": "cisco", "connector": "cisco_nxos", "os": "nx-os"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.FetchOptions().Cache != nil {
		t.Fatal("cache_enabled: false ignored")
	}
	o := c.Inventory.RuleOptions()
	if len(o.Mappings) != 1 || *o.Mappings[0].OS != "nx-os" || o.Selection.IncludeVendors[0] != "cisco" {
		t.Fatalf("%+v", o)
	}
}

func TestValidateRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"source":       "netdisco: {source: ldap}",
		"plain http":   "netdisco: {url: 'http://nd.example'}",
		"url creds":    "netdisco: {url: 'https://u:p@nd.example'}",
		"cache only":   "netdisco: {cache_mode: only, cache_enabled: false}",
		"query":        "netdisco: {source: postgres}",
		"port":         "netdisco: {source: postgres, query: q, db_port: 70000}",
		"sslmode":      "netdisco: {db_sslmode: maybe}",
		"timeout":      "netdisco: {inventory_timeout: 0}",
		"unknown key":  "netdisco: {bogus: 1}",
		"empty rule":   "inventory: {device_rules: [{os: x}]}",
		"query params": "netdisco: {query_params: [{a: 1}]}",
	} {
		if _, err := inventory.DecodeYAML([]byte(doc)); !errors.Is(err, inventory.ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := inventory.DecodeYAML([]byte("netdisco: {url: 'http://nd.example', allow_insecure_http: true}")); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresDSNQuotes(t *testing.T) {
	c, _ := inventory.DecodeYAML([]byte("netdisco: {source: postgres, query: q, db_port: 5433, db_sslmode: require}"))
	dsn, err := c.PostgresDSN(inventory.Credentials{DBHost: "db", DBName: "nd", DBUser: "o'brien", DBPassword: `p\w`})
	if err != nil {
		t.Fatal(err)
	}
	if want := `host='db' dbname='nd' user='o\'brien' port='5433' sslmode='require' password='p\\w'`; dsn != want {
		t.Fatalf("%s", dsn)
	}
	if _, err := c.PostgresDSN(inventory.Credentials{DBHost: "db"}); !errors.Is(err, inventory.ErrInvalid) {
		t.Fatal("missing credentials accepted")
	}
	only, _ := inventory.DecodeYAML([]byte("netdisco: {source: postgres, query: q, cache_mode: only}"))
	if dsn, _ := only.PostgresDSN(inventory.Credentials{DBHost: "a", DBName: "b", DBUser: "c", DBPassword: "secret"}); dsn != `host='a' dbname='b' user='c'` {
		t.Fatalf("cache-only DSN carries password: %s", dsn)
	}
}

func TestDevicesResolvesPlatform(t *testing.T) {
	c, _ := inventory.DecodeYAML([]byte("inventory: {device_rules: [{vendor: cisco, connector: cisco_nxos}]}"))
	rules, err := c.Rules(vendors.NewBuiltinRegistry())
	if err != nil {
		t.Fatal(err)
	}
	got := inventory.Devices(rules, []ncinventory.Device{
		{IP: "10.0.0.1", Vendor: "cisco", Model: "N9K"},
		{IP: "not-an-ip", Vendor: "cisco"},
		{IP: "10.0.0.2", Name: "leaf2", Vendor: "cisco"},
	}, "cisco_nxos")
	if len(got) != 2 || got[0].Name != "10.0.0.1" || got[1].Name != "leaf2" || got[0].Platform != "cisco_nxos" {
		t.Fatalf("%+v", got)
	}
}
