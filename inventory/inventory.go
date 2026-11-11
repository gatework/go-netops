// Package inventory turns the shared netdisco/inventory configuration into
// typed connector options and fetches the device list.
//
// go-vxlan and go-wireless read the same keys; this package is the single
// typed decoder and validator for them. Layering (file, environment, flags)
// stays with each application, which typically merges layers with
// go-net-connector's config.InventoryEnvironment and passes the result to
// Decode. Credentials are supplied explicitly and never read here.
package inventory

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/gatework/go-net-connector/fleet"
	ncinventory "github.com/gatework/go-net-connector/inventory"
	"github.com/gatework/go-net-connector/netdisco"
	"github.com/gatework/go-net-connector/vendors/dialect"
)

// ErrInvalid reports an invalid inventory configuration.
var ErrInvalid = errors.New("inventory: invalid configuration")

// Netdisco selects and bounds the device source.
type Netdisco struct {
	Source            string `yaml:"source"` // http (default) or postgres
	URL               string `yaml:"url"`
	AllowInsecureHTTP bool   `yaml:"allow_insecure_http"`
	Query             string `yaml:"query"`
	QueryParams       []any  `yaml:"query_params"`

	CacheMode      string   `yaml:"cache_mode"` // prefer (default), refresh, only
	CacheEnabled   *bool    `yaml:"cache_enabled"`
	CacheTTL       *float64 `yaml:"cache_ttl"` // seconds
	CacheDirectory string   `yaml:"cache_directory"`
	CacheNamespace string   `yaml:"cache_namespace"`

	PageSize          *int     `yaml:"page_size"`
	MaxPages          *int     `yaml:"max_pages"`
	MaxDevices        *int     `yaml:"max_devices"`
	MaxResponseBytes  *int64   `yaml:"max_response_bytes"`
	MaxInventoryBytes *int64   `yaml:"max_inventory_bytes"`
	InventoryTimeout  *float64 `yaml:"inventory_timeout"` // seconds

	DBPort           *int   `yaml:"db_port"`
	DBSSLMode        string `yaml:"db_sslmode"`
	DBSSLRootCert    string `yaml:"db_sslrootcert"`
	DBConnectTimeout *int   `yaml:"db_connect_timeout"`
}

// DeviceRule maps inventory rows to a connector platform.
type DeviceRule struct {
	Vendor      string  `yaml:"vendor"`
	Connector   string  `yaml:"connector"`
	OS          *string `yaml:"os"`
	ModelPrefix *string `yaml:"model_prefix"`
}

// Selection filters inventory rows.
type Selection struct {
	IncludeHosts    []string          `yaml:"include_hosts"`
	ExcludeHosts    []string          `yaml:"exclude_hosts"`
	IncludeNames    []string          `yaml:"include_names"`
	ExcludeNames    []string          `yaml:"exclude_names"`
	IncludeVendors  []string          `yaml:"include_vendors"`
	ExcludeVendors  []string          `yaml:"exclude_vendors"`
	IncludeModels   []string          `yaml:"include_models"`
	ExcludeModels   []string          `yaml:"exclude_models"`
	NameMatch       string            `yaml:"name_match"`
	ModelMatch      string            `yaml:"model_match"`
	VendorOverrides map[string]string `yaml:"vendor_overrides"`
	HostOverrides   map[string]string `yaml:"host_overrides"`
	DeviceRules     []DeviceRule      `yaml:"device_rules"`
}

// Config is the typed form of the netdisco and inventory sections.
type Config struct {
	Netdisco  Netdisco  `yaml:"netdisco"`
	Inventory Selection `yaml:"inventory"`
}

// Credentials authenticate the source; the application reads them from its
// environment layer.
type Credentials struct {
	Username, Password, APIKey         string // HTTP
	DBHost, DBName, DBUser, DBPassword string // PostgreSQL
}

// Decode strictly decodes merged configuration layers, such as the value
// produced by config.InventoryEnvironment, and validates it.
func Decode(data map[string]any) (Config, error) {
	body, err := yaml.Marshal(data)
	if err != nil {
		return Config{}, ErrInvalid
	}
	return DecodeYAML(body)
}

// DecodeYAML strictly decodes and validates a YAML document.
func DecodeYAML(body []byte) (Config, error) {
	var c Config
	d := yaml.NewDecoder(bytes.NewReader(body))
	d.KnownFields(true)
	if err := d.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	return c, c.Validate()
}

func invalid(field string) error { return fmt.Errorf("%w: %s", ErrInvalid, field) }

// Validate checks values the connector would otherwise reject late or
// accept unsafely.
func (c Config) Validate() error {
	n := c.Netdisco
	for _, check := range []func() error{n.validateSource, n.validateURL, n.validateDatabase, n.validateQueryParams} {
		if err := check(); err != nil {
			return err
		}
	}
	if _, err := c.Limits(); err != nil {
		return err
	}
	for _, r := range c.Inventory.DeviceRules {
		if r.Vendor == "" && r.Connector == "" {
			return invalid("inventory.device_rules")
		}
	}
	return nil
}

// validateSource checks the source, cache mode and local paths.
func (n Netdisco) validateSource() error {
	if !slices.Contains([]string{"", "http", "postgres"}, n.Source) {
		return invalid("netdisco.source")
	}
	if !slices.Contains([]string{"", "prefer", "refresh", "only"}, n.CacheMode) {
		return invalid("netdisco.cache_mode")
	}
	if n.CacheEnabled != nil && !*n.CacheEnabled && n.CacheMode == "only" {
		return invalid("netdisco.cache_enabled")
	}
	if strings.ContainsRune(n.CacheDirectory, 0) || strings.ContainsRune(n.DBSSLRootCert, 0) {
		return invalid("netdisco paths")
	}
	return nil
}

// validateURL requires an absolute http(s) URL without credentials, query or
// fragment, and https unless insecure HTTP was allowed explicitly.
func (n Netdisco) validateURL() error {
	if n.URL == "" {
		return nil
	}
	u, err := url.Parse(n.URL)
	if err != nil || u.Host == "" || !slices.Contains([]string{"http", "https"}, u.Scheme) || u.User != nil ||
		u.RawQuery != "" || u.Fragment != "" || strings.IndexFunc(n.URL, unicode.IsControl) >= 0 {
		return invalid("netdisco.url")
	}
	if !n.AllowInsecureHTTP && u.Scheme != "https" {
		return invalid("netdisco.url requires https")
	}
	return nil
}

// validateDatabase checks the Postgres query and connection settings.
func (n Netdisco) validateDatabase() error {
	if n.Source == "postgres" && (strings.TrimSpace(n.Query) == "" || strings.ContainsRune(n.Query, 0)) {
		return invalid("netdisco.query")
	}
	if n.DBPort != nil && (*n.DBPort < 1 || *n.DBPort > 65535) {
		return invalid("netdisco.db_port")
	}
	if n.DBConnectTimeout != nil && *n.DBConnectTimeout < 0 {
		return invalid("netdisco.db_connect_timeout")
	}
	if !slices.Contains([]string{"", "disable", "allow", "prefer", "require", "verify-ca", "verify-full"}, n.DBSSLMode) {
		return invalid("netdisco.db_sslmode")
	}
	return nil
}

// validateQueryParams allows only scalar query parameters without NUL.
func (n Netdisco) validateQueryParams() error {
	for _, v := range n.QueryParams {
		switch v := v.(type) {
		case string:
			if strings.ContainsRune(v, 0) {
				return invalid("netdisco.query_params")
			}
		case nil, bool, int, float64:
		default:
			return invalid("netdisco.query_params")
		}
	}
	return nil
}

// Limits returns the source limits with the shared defaults.
func (c Config) Limits() (netdisco.Limits, error) {
	n := c.Netdisco
	l := netdisco.Limits{PageSize: 500, MaxPages: 10000, MaxDevices: 100000, MaxResponseBytes: 16 << 20, MaxInventoryBytes: 128 << 20, Timeout: 300 * time.Second}
	set := func(dst *int, v *int) {
		if v != nil {
			*dst = *v
		}
	}
	set(&l.PageSize, n.PageSize)
	set(&l.MaxPages, n.MaxPages)
	set(&l.MaxDevices, n.MaxDevices)
	if n.MaxResponseBytes != nil {
		l.MaxResponseBytes = *n.MaxResponseBytes
	}
	if n.MaxInventoryBytes != nil {
		l.MaxInventoryBytes = *n.MaxInventoryBytes
	}
	if n.InventoryTimeout != nil {
		f := *n.InventoryTimeout
		if f <= 0 || f > float64(int64(^uint64(0)>>1))/1e9 {
			return l, invalid("netdisco.inventory_timeout")
		}
		l.Timeout = time.Duration(f * 1e9)
	}
	if l.PageSize < 0 || l.MaxPages < 0 || l.MaxDevices < 0 || l.MaxResponseBytes < 0 || l.MaxInventoryBytes < 0 {
		return l, invalid("netdisco limits")
	}
	return l, nil
}

// FetchOptions returns the cache mode and policy. The cache is enabled by
// default with a 72-hour TTL.
func (c Config) FetchOptions() netdisco.FetchOptions {
	n := c.Netdisco
	o := netdisco.FetchOptions{Mode: netdisco.CacheMode(n.CacheMode)}
	if o.Mode == "" {
		o.Mode = netdisco.CachePrefer
	}
	if n.CacheEnabled == nil || *n.CacheEnabled {
		ttl := 72 * time.Hour
		if n.CacheTTL != nil {
			ttl = time.Duration(*n.CacheTTL * float64(time.Second))
		}
		o.Cache = &netdisco.CachePolicy{Directory: n.CacheDirectory, Namespace: n.CacheNamespace, TTL: &ttl}
	}
	return o
}

// RuleOptions converts the selection to connector fleet options.
func (s Selection) RuleOptions() fleet.RuleOptions {
	o := fleet.RuleOptions{
		Selection: ncinventory.SelectionOptions{
			IncludeHosts: s.IncludeHosts, ExcludeHosts: s.ExcludeHosts,
			IncludeNames: s.IncludeNames, ExcludeNames: s.ExcludeNames,
			IncludeVendors: s.IncludeVendors, ExcludeVendors: s.ExcludeVendors,
			IncludeModels: s.IncludeModels, ExcludeModels: s.ExcludeModels,
			NameMatch: s.NameMatch, ModelMatch: s.ModelMatch,
		},
		VendorOverrides: s.VendorOverrides,
		HostOverrides:   s.HostOverrides,
	}
	for _, r := range s.DeviceRules {
		o.Mappings = append(o.Mappings, fleet.Mapping{Vendor: r.Vendor, Connector: r.Connector, OS: r.OS, ModelPrefix: r.ModelPrefix})
	}
	return o
}

// Rules builds the fleet resolver against an explicit platform registry.
func (c Config) Rules(registry dialect.Registry) (*fleet.Rules, error) {
	return fleet.NewRules(c.Inventory.RuleOptions(), registry)
}

// PostgresDSN builds a libpq keyword/value connection string with every value
// quoted. The password is omitted in cache-only mode, which never connects.
func (c Config) PostgresDSN(cred Credentials) (string, error) {
	n := c.Netdisco
	var parts []string
	add := func(key, value string) { parts = append(parts, key+"="+quoteDSN(value)) }
	for _, kv := range [][2]string{{"host", cred.DBHost}, {"dbname", cred.DBName}, {"user", cred.DBUser}} {
		if kv[1] == "" {
			return "", invalid("postgres credentials")
		}
		add(kv[0], kv[1])
	}
	if n.DBPort != nil {
		add("port", strconv.Itoa(*n.DBPort))
	}
	if n.DBSSLMode != "" {
		add("sslmode", n.DBSSLMode)
	}
	if n.DBSSLRootCert != "" {
		add("sslrootcert", n.DBSSLRootCert)
	}
	if n.DBConnectTimeout != nil {
		add("connect_timeout", strconv.Itoa(*n.DBConnectTimeout))
	}
	if n.CacheMode != "only" {
		add("password", cred.DBPassword)
	}
	return strings.Join(parts, " "), nil
}

func quoteDSN(v string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), "'", `\'`) + "'"
}

// Fetch reads the inventory from the configured source, honouring the cache.
func (c Config) Fetch(ctx context.Context, cred Credentials) (netdisco.CacheResult, error) {
	limits, err := c.Limits()
	if err != nil {
		return netdisco.CacheResult{}, err
	}
	n := c.Netdisco
	switch n.Source {
	case "", "http":
		return netdisco.FetchHTTP(ctx, netdisco.HTTPOptions{URL: n.URL, Username: cred.Username, Password: cred.Password, APIKey: cred.APIKey, RequireHTTPS: !n.AllowInsecureHTTP, Limits: limits}, c.FetchOptions())
	case "postgres":
		dsn, err := c.PostgresDSN(cred)
		if err != nil {
			return netdisco.CacheResult{}, err
		}
		return netdisco.FetchPostgres(ctx, netdisco.PostgresOptions{Connection: dsn, Query: n.Query, Parameters: n.QueryParams, Limits: limits}, c.FetchOptions())
	}
	return netdisco.CacheResult{}, invalid("netdisco.source")
}

// Device is one resolved inventory row.
type Device struct {
	Host, Name, Model, Platform string
}

// Devices resolves rows to devices of the wanted connector platform. A row
// without a name is named by its host; invalid rows are skipped.
func Devices(rules *fleet.Rules, rows []ncinventory.Device, platform string) []Device {
	var out []Device
	for _, row := range rows {
		r := rules.Record(row)
		if r.Issue != "" || r.Vendor != platform {
			continue
		}
		name := r.Name
		if name == "" {
			name = r.Host
		}
		out = append(out, Device{Host: r.Host, Name: name, Model: r.Model, Platform: r.Vendor})
	}
	return out
}
