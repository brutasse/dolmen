// Package config loads and validates the service YAML configuration.
package config

import (
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the gist configuration.
type Config struct {
	// Listen is the address to serve on, e.g. ":8080". Defaults to ":8080".
	Listen string `yaml:"listen"`
	// BaseURL is the externally reachable origin used to build the OAuth
	// redirect URI. Defaults to http://localhost<listen>.
	BaseURL string     `yaml:"base_url"`
	OIDC    OIDCConfig `yaml:"oidc"`
	S3      S3Config   `yaml:"s3"`
}

// OIDCConfig configures the authorization-code + PKCE login flow. The flow
// is only available when ClientSecret is set; without it the app verifies
// tokens issued by Issuer for ClientID but cannot run its own login.
type OIDCConfig struct {
	Issuer   string `yaml:"issuer"`
	ClientID string `yaml:"client_id"`
	// ClientSecret is the name of an environment variable holding the
	// client secret. Load resolves it; the struct holds the value.
	ClientSecret string `yaml:"client_secret"`
}

// S3Config configures the storage backend. Credentials are not part of the
// configuration: the standard AWS SDK credential chain (environment
// variables, shared config file, instance profile) applies.
type S3Config struct {
	Bucket string `yaml:"bucket"`
	// Endpoint points at an S3-compatible provider (Exoscale SOS, s3mock,
	// ...). Empty means AWS.
	Endpoint string `yaml:"endpoint"`
	// CacheSize bounds the in-memory read cache, as a byte size such as
	// "128MB" or "512MiB"; a bare number is bytes. Empty means
	// DefaultCacheSize, "0" turns the cache off.
	CacheSize string `yaml:"cache_size"`
}

// DefaultCacheSize bounds the read cache when CacheSize is unset. Typical
// gist objects are a few KiB, so this holds a large personal collection.
const DefaultCacheSize = "128MB"

var sizeUnits = map[string]int64{
	"":    1,
	"b":   1,
	"kb":  1e3,
	"mb":  1e6,
	"gb":  1e9,
	"tb":  1e12,
	"kib": 1 << 10,
	"mib": 1 << 20,
	"gib": 1 << 30,
	"tib": 1 << 40,
}

// CacheMaxBytes resolves CacheSize to a byte budget. An empty CacheSize is
// DefaultCacheSize; 0 means no cache. Load validates the string, but this
// stays available for a Config assembled in code, so callers that build
// S3Config by hand get the default too.
func (s S3Config) CacheMaxBytes() (int64, error) {
	size := strings.TrimSpace(s.CacheSize)
	if size == "" {
		size = DefaultCacheSize
	}
	digits := 0
	for digits < len(size) && size[digits] >= '0' && size[digits] <= '9' {
		digits++
	}
	if digits == 0 {
		return 0, fmt.Errorf("s3.cache_size %q is not a byte size: expected a number like 128MB or 512MiB", s.CacheSize)
	}
	n, err := strconv.ParseInt(size[:digits], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("s3.cache_size %q: %w", s.CacheSize, err)
	}
	unit := strings.ToLower(strings.TrimSpace(size[digits:]))
	mult, ok := sizeUnits[unit]
	if !ok {
		return 0, fmt.Errorf("s3.cache_size %q: unknown unit %q (use B, KB, MB, GB, TB or the KiB/MiB/GiB/TiB forms)", s.CacheSize, unit)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("s3.cache_size %q overflows int64 bytes", s.CacheSize)
	}
	return n * mult, nil
}

// Load reads and validates the YAML config at path, resolving secret
// environment-variable references into their values.
func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) validate() error {
	var errs []error
	add := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf(format, args...))
	}

	if c.Listen == "" {
		c.Listen = ":8080"
	}
	if c.BaseURL == "" {
		host := c.Listen
		if strings.HasPrefix(host, ":") {
			host = "localhost" + host
		}
		c.BaseURL = "http://" + host
	}

	if c.OIDC.Issuer == "" {
		add("oidc.issuer is required")
	} else if u, err := url.Parse(c.OIDC.Issuer); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		add("oidc.issuer must be an http(s) URL")
	}
	if c.OIDC.ClientID == "" {
		add("oidc.client_id is required")
	}
	// An empty client_secret disables the built-in login flow: the app
	// then only verifies tokens an authenticating proxy forwards, which
	// needs the issuer and client id but no secret.
	if c.OIDC.ClientSecret != "" {
		v, ok := os.LookupEnv(c.OIDC.ClientSecret)
		if !ok || v == "" {
			add("oidc.client_secret: environment variable %q is not set or empty", c.OIDC.ClientSecret)
		} else {
			c.OIDC.ClientSecret = v
		}
	}

	if c.S3.Bucket == "" {
		add("s3.bucket is required")
	}
	if _, err := c.S3.CacheMaxBytes(); err != nil {
		add("%v", err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
