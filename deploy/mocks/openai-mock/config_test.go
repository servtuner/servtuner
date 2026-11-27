package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestValidateRejectsMixedAndIncompleteTransport(t *testing.T) {
	cases := map[string]func(*Config){
		"mtls without any files":  func(c *Config) { c.TLSMode = tlsModeMutual },
		"mtls without client CA":  func(c *Config) { c.TLSMode = tlsModeMutual; c.CertFile = "a"; c.KeyFile = "b" },
		"platform with cert file": func(c *Config) { c.TLSMode = tlsModePlatform; c.CertFile = "a" },
		"platform with client CA": func(c *Config) { c.TLSMode = tlsModePlatform; c.ClientCAFile = "a" },
		"unknown tls mode":        func(c *Config) { c.TLSMode = "auto" },
		"empty tls mode":          func(c *Config) { c.TLSMode = "" },
		"negative ttft":           func(c *Config) { c.TLSMode = tlsModePlatform; c.TimeToFirstToken = -time.Second },
		"zero output tokens":      func(c *Config) { c.TLSMode = tlsModePlatform; c.OutputTokens = 0 },
		"empty model":             func(c *Config) { c.TLSMode = tlsModePlatform; c.ModelName = "" },
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
		"OPENAI_MOCK_TLS_MODE":              "platform",
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
	if _, err := loadConfig([]string{"--tls-mode", "platform", "--ttft", "fast"}, noEnv, io.Discard); err == nil {
		t.Error("expected a bad --ttft to fail")
	}
	badEnv := func(k string) string {
		if k == "OPENAI_MOCK_OUTPUT_TOKENS" {
			return "many"
		}
		return ""
	}
	_, err := loadConfig([]string{"--tls-mode", "platform"}, badEnv, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "OPENAI_MOCK_OUTPUT_TOKENS") {
		t.Errorf("expected the bad variable to be named, got %v", err)
	}
	if _, err := loadConfig([]string{"--tls-mode", "platform", "--tls-cert-file", "x"}, noEnv, io.Discard); err == nil {
		t.Error("expected platform with a cert file to fail")
	}
	if _, err := loadConfig(nil, noEnv, io.Discard); err == nil {
		t.Error("expected the mtls default without cert files to fail")
	}
}

func TestValidateRejectsMetricsListenEqualToListen(t *testing.T) {
	cfg := platformConfig()
	cfg.MetricsListenAddress = cfg.ListenAddress
	if cfg.Validate() == nil {
		t.Fatal("metrics-listen equal to listen must be rejected")
	}
}
