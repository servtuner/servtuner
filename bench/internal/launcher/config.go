package launcher

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/goccy/go-yaml"
)

// Config is servtuner launcher's configuration file (deploy/launcher/README.md). Every field matches a
// flag of the same name, and a flag given on the command line wins over the file. Secrets are
// never in it: token_file and api_key_file name files that hold them.
type Config struct {
	Listen string `yaml:"listen"`
	// TLS is "true" or "false": whether the launcher API is served over TLS. Required, never inferred.
	TLS string `yaml:"tls"`
	// ClientAuth is the client-certificate policy with TLS: required, optional or none.
	ClientAuth      string `yaml:"client_auth"`
	TLSCertFile     string `yaml:"tls_cert_file"`
	TLSKeyFile      string `yaml:"tls_key_file"`
	TLSClientCAFile string `yaml:"tls_client_ca_file"`
	TokenFile       string `yaml:"token_file"`
	APIKeyFile      string `yaml:"api_key_file"`
	VLLM            string `yaml:"vllm"`
	LogDir          string `yaml:"log_dir"`
}

const (
	// ConfigEnv names a config file, like --config.
	ConfigEnv = "SERVTUNER_LAUNCHER_CONFIG"
	// SystemConfigPath is a system service's config (a bare-metal host, a dedicated user).
	SystemConfigPath = "/etc/servtuner/launcher.yaml"
	// CredentialsDirectoryEnv is where systemd puts LoadCredential= files for this service only.
	CredentialsDirectoryEnv = "CREDENTIALS_DIRECTORY"
	// The credential names a unit's LoadCredential= gives (deploy/launcher/servtuner-launcher-system.service).
	TokenCredential  = "launcher-token"
	APIKeyCredential = "vllm-api-key"
)

// ConfigPath finds the config file: --config, then $SERVTUNER_LAUNCHER_CONFIG, then the user's
// ($XDG_CONFIG_HOME/servtuner/launcher.yaml, ~/.config by default), then the system's. A file named
// explicitly must exist; the default places are skipped when absent. "" means no file.
func ConfigPath(flagPath string, getenv func(string) string, exists func(string) bool) (string, error) {
	for _, explicit := range []struct{ path, from string }{
		{flagPath, "--config"},
		{getenv(ConfigEnv), "$" + ConfigEnv},
	} {
		if explicit.path == "" {
			continue
		}
		if !exists(explicit.path) {
			return "", fmt.Errorf("%s %s: no such file", explicit.from, explicit.path)
		}
		return explicit.path, nil
	}
	for _, candidate := range []string{userConfigPath(getenv), SystemConfigPath} {
		if candidate != "" && exists(candidate) {
			return candidate, nil
		}
	}
	return "", nil
}

// userConfigPath follows the XDG base directory spec: $XDG_CONFIG_HOME, else $HOME/.config.
func userConfigPath(getenv func(string) string) string {
	base := getenv("XDG_CONFIG_HOME")
	if base == "" {
		home := getenv("HOME")
		if home == "" {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "servtuner", "launcher.yaml")
}

// LoadConfig reads a config file, refusing keys it does not know so a typo is never ignored.
func LoadConfig(path string) (Config, error) {
	var config Config
	if path == "" {
		return config, nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return config, fmt.Errorf("read config %s: %w", path, err)
	}
	var keys map[string]any
	if yaml.Unmarshal(content, &keys) == nil {
		if mode, found := keys["tls_mode"]; found {
			replacement := "tls: true, with client_auth: required, optional or none"
			if mode == "platform" {
				replacement = "tls: false"
			}
			return config, fmt.Errorf("config %s: tls_mode was replaced in servtuner 0.2: use %s", path, replacement)
		}
	}
	if err := yaml.UnmarshalWithOptions(content, &config, yaml.DisallowUnknownField()); err != nil {
		return config, fmt.Errorf("config %s: %w", path, err)
	}
	return config, nil
}

// WithCredentialDefaults fills token_file and api_key_file from systemd's credentials directory
// when the config and flags left them empty, so a unit's LoadCredential= needs no other setting.
// The API key is optional, so its default applies only when that credential exists.
func (c Config) WithCredentialDefaults(getenv func(string) string, exists func(string) bool) Config {
	directory := getenv(CredentialsDirectoryEnv)
	if directory == "" {
		return c
	}
	if c.TokenFile == "" {
		c.TokenFile = filepath.Join(directory, TokenCredential)
	}
	if apiKey := filepath.Join(directory, APIKeyCredential); c.APIKeyFile == "" && exists(apiKey) {
		c.APIKeyFile = apiKey
	}
	return c
}

// FileExists reports whether path names an existing file (the default for ConfigPath).
func FileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
