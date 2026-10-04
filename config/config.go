// Package config loads and validates the service YAML configuration.
package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config is the gist configuration.
type Config struct {
	// Listen is the address to serve on, e.g. ":8080". Defaults to ":8080".
	Listen string `yaml:"listen"`
	// BaseURL is the externally reachable origin used to build the OAuth
	// redirect URI. Defaults to http://localhost<listen>.
	BaseURL        string     `yaml:"base_url"`
	AuthCookieName string     `yaml:"auth_cookie_name"`
	OIDC           OIDCConfig `yaml:"oidc"`
	S3             S3Config   `yaml:"s3"`
}

// OIDCConfig configures the authorization-code + PKCE login flow.
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
	switch {
	case c.OIDC.ClientSecret == "":
		add("oidc.client_secret is required (name of the env var holding the secret)")
	default:
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

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}
