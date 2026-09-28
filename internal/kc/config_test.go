package kc

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validConfig() Config {
	return Config{
		BaseURL:      "https://kc.example.com",
		Realm:        "employees",
		ClientID:     "kcac-audit",
		ClientSecret: "s3cret",
	}
}

func TestConfigValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{name: "valid", mutate: func(*Config) {}},
		{name: "missing url", mutate: func(c *Config) { c.BaseURL = "" }, wantErr: "url is required"},
		{name: "blank url", mutate: func(c *Config) { c.BaseURL = "   " }, wantErr: "url is required"},
		{name: "bad scheme", mutate: func(c *Config) { c.BaseURL = "ftp://kc" }, wantErr: "scheme must be http or https"},
		{name: "no host", mutate: func(c *Config) { c.BaseURL = "https://" }, wantErr: "no host"},
		{name: "missing realm", mutate: func(c *Config) { c.Realm = "" }, wantErr: "realm is required"},
		{name: "missing client id", mutate: func(c *Config) { c.ClientID = "" }, wantErr: "client-id is required"},
		{name: "missing secret", mutate: func(c *Config) { c.ClientSecret = "" }, wantErr: "KCAC_CLIENT_SECRET"},
		{name: "negative concurrency", mutate: func(c *Config) { c.Concurrency = -1 }, wantErr: "concurrency must not be negative"},
		{name: "negative page size", mutate: func(c *Config) { c.PageSize = -5 }, wantErr: "page-size must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := validConfig()
			tt.mutate(&c)
			err := c.Validate()

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want error containing %q", tt.wantErr)
			}
			if !errors.Is(err, ErrMissingField) {
				t.Errorf("error should wrap ErrMissingField, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestConfigValidateReportsEveryProblemAtOnce(t *testing.T) {
	// One round trip of feedback beats one problem per run.
	c := Config{}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error")
	}
	for _, want := range []string{"url is required", "realm is required", "client-id is required", "KCAC_CLIENT_SECRET"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestConfigWithDefaultsDoesNotMutateReceiver(t *testing.T) {
	c := validConfig()
	c.BaseURL = "https://kc.example.com/  "
	got := c.WithDefaults()

	if c.Concurrency != 0 || c.PageSize != 0 {
		t.Error("WithDefaults must not mutate the receiver")
	}
	if got.Concurrency != DefaultConcurrency {
		t.Errorf("Concurrency = %d, want %d", got.Concurrency, DefaultConcurrency)
	}
	if got.PageSize != DefaultPageSize {
		t.Errorf("PageSize = %d, want %d", got.PageSize, DefaultPageSize)
	}
	if got.MaxRetries != DefaultMaxRetries {
		t.Errorf("MaxRetries = %d, want %d", got.MaxRetries, DefaultMaxRetries)
	}
	if got.RequestTimeout != DefaultRequestTimeout {
		t.Errorf("RequestTimeout = %v, want %v", got.RequestTimeout, DefaultRequestTimeout)
	}
	if got.BaseURL != "https://kc.example.com" {
		t.Errorf("BaseURL = %q, want trailing slash and space trimmed", got.BaseURL)
	}
}

func TestConfigWithDefaultsPreservesExplicitValues(t *testing.T) {
	c := validConfig()
	c.Concurrency = 1
	c.PageSize = 25
	c.MaxRetries = 9
	c.RequestTimeout = time.Second
	got := c.WithDefaults()

	if got.Concurrency != 1 || got.PageSize != 25 || got.MaxRetries != 9 || got.RequestTimeout != time.Second {
		t.Errorf("explicit values overwritten: %+v", got)
	}
}

func TestConfigURLs(t *testing.T) {
	c := validConfig().WithDefaults()

	if got, want := c.tokenURL(), "https://kc.example.com/realms/employees/protocol/openid-connect/token"; got != want {
		t.Errorf("tokenURL() = %q, want %q", got, want)
	}
	if got, want := c.adminPath("/users/count"), "https://kc.example.com/admin/realms/employees/users/count"; got != want {
		t.Errorf("adminPath() = %q, want %q", got, want)
	}
	if got, want := c.adminPath("/groups/%s/children", "g1"), "https://kc.example.com/admin/realms/employees/groups/g1/children"; got != want {
		t.Errorf("adminPath() = %q, want %q", got, want)
	}
}

func TestConfigRealmNameIsEscaped(t *testing.T) {
	// Realm names can contain characters that must not break out of the path.
	c := Config{BaseURL: "https://kc.example.com", Realm: "my realm/x"}.WithDefaults()
	got := c.adminPath("/users")
	if strings.Contains(got, "my realm") || strings.Contains(got, "realm/x/users") {
		t.Errorf("realm name not escaped: %q", got)
	}
}

func TestConfigPreservesLegacyAuthSuffix(t *testing.T) {
	// Keycloak before 17 served the admin API under /auth.
	c := Config{BaseURL: "https://kc.example.com/auth", Realm: "employees"}.WithDefaults()
	if got, want := c.adminPath("/users"), "https://kc.example.com/auth/admin/realms/employees/users"; got != want {
		t.Errorf("adminPath() = %q, want %q", got, want)
	}
}
