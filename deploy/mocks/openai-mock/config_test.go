package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsMixedAndIncompleteTransport(t *testing.T) {
	cases := map[string]func(*Config){
		"tls without any files":      func(c *Config) { c.ClientAuth = clientAuthRequired },
		"required without client CA": func(c *Config) { c.ClientAuth = clientAuthRequired; c.CertFile = "a"; c.KeyFile = "b" },
		"optional without client CA": func(c *Config) { c.ClientAuth = clientAuthOptional; c.CertFile = "a"; c.KeyFile = "b" },
		"none with a client CA": func(c *Config) {
			c.ClientAuth = clientAuthNone
			c.CertFile = "a"
			c.KeyFile = "b"
			c.ClientCAFile = "c"
		},
		"unknown client auth":    func(c *Config) { c.ClientAuth = "sometimes"; c.CertFile = "a"; c.KeyFile = "b" },
		"plain with cert file":   func(c *Config) { c.TLS = tlsOff; c.CertFile = "a" },
		"plain with client CA":   func(c *Config) { c.TLS = tlsOff; c.ClientCAFile = "a" },
		"plain with client auth": func(c *Config) { c.TLS = tlsOff; c.ClientAuth = clientAuthNone },
		"tls not a boolean":      func(c *Config) { c.TLS = "auto" },
		"empty tls":              func(c *Config) { c.TLS = "" },
		"0.1 mode name":          func(c *Config) { c.TLS = "platform" },
		"negative ttft":          func(c *Config) { c.TLS = tlsOff; c.TimeToFirstToken = -time.Second },
		"zero output tokens":     func(c *Config) { c.TLS = tlsOff; c.OutputTokens = 0 },
		"empty model":            func(c *Config) { c.TLS = tlsOff; c.ModelName = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := defaultConfig()
			mutate(&cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}

func TestLoadConfigFlagsOverrideEnvironment(t *testing.T) {
	environment := map[string]string{
		"OPENAI_MOCK_TLS":                   "false",
		"OPENAI_MOCK_TTFT":                  "1s",
		"OPENAI_MOCK_MODEL":                 "from-env",
		"OPENAI_MOCK_ROLE_ONLY_FIRST_CHUNK": "true",
	}
	cfg, err := loadConfig([]string{"--model", "from-flag", "--itl", "5ms"}, func(k string) string { return environment[k] }, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ModelName != "from-flag" || cfg.TimeToFirstToken != time.Second || cfg.InterTokenLatency != 5*time.Millisecond || !cfg.SendRoleOnlyFirstChunk {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRejectsBadEnvironmentAndMixedModes(t *testing.T) {
	noEnv := func(string) string { return "" }
	if _, err := loadConfig([]string{"--tls", "false", "--ttft", "fast"}, noEnv, io.Discard); err == nil {
		t.Error("expected a bad --ttft to fail")
	}
	badEnv := func(k string) string {
		if k == "OPENAI_MOCK_OUTPUT_TOKENS" {
			return "many"
		}
		return ""
	}
	_, err := loadConfig([]string{"--tls", "false"}, badEnv, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "OPENAI_MOCK_OUTPUT_TOKENS") {
		t.Errorf("expected the bad variable to be named, got %v", err)
	}
	if _, err := loadConfig([]string{"--tls", "false", "--tls-cert-file", "x"}, noEnv, io.Discard); err == nil {
		t.Error("expected --tls false with a cert file to fail")
	}
	if _, err := loadConfig(nil, noEnv, io.Discard); err == nil {
		t.Error("expected the TLS default (client certificates required) without cert files to fail")
	}
}

func TestValidateRejectsMetricsListenEqualToListen(t *testing.T) {
	cfg := plainConfig()
	cfg.MetricsListenAddress = cfg.ListenAddress
	if cfg.Validate() == nil {
		t.Fatal("metrics-listen equal to listen must be rejected")
	}
}
