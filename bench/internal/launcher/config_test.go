package launcher

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func environment(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func existing(paths ...string) func(string) bool {
	return func(path string) bool {
		for _, candidate := range paths {
			if candidate == path {
				return true
			}
		}
		return false
	}
}

func TestConfigPathFollowsTheOrder(t *testing.T) {
	user := "/home/u/.config/servtuner/launcher.yaml"
	everywhere := existing("/flag.yaml", "/env.yaml", "/xdg/servtuner/launcher.yaml", user, SystemConfigPath)
	cases := []struct {
		name, flag string
		env        map[string]string
		exists     func(string) bool
		want       string
	}{
		{"the flag first", "/flag.yaml", map[string]string{ConfigEnv: "/env.yaml"}, everywhere, "/flag.yaml"},
		{"then the variable", "", map[string]string{ConfigEnv: "/env.yaml", "HOME": "/home/u"}, everywhere, "/env.yaml"},
		{"then XDG_CONFIG_HOME", "", map[string]string{"XDG_CONFIG_HOME": "/xdg", "HOME": "/home/u"}, everywhere,
			"/xdg/servtuner/launcher.yaml"},
		{"then ~/.config", "", map[string]string{"HOME": "/home/u"}, everywhere, user},
		{"then the system's", "", map[string]string{"HOME": "/home/u"}, existing(SystemConfigPath), SystemConfigPath},
		{"or none", "", map[string]string{"HOME": "/home/u"}, existing(), ""},
	}
	for _, c := range cases {
		got, err := ConfigPath(c.flag, environment(c.env), c.exists)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q, %v; want %q", c.name, got, err, c.want)
		}
	}
}

func TestANamedConfigMustExist(t *testing.T) {
	for _, c := range []struct{ flag, env, from string }{
		{"/missing.yaml", "", "--config"},
		{"", "/missing.yaml", "$" + ConfigEnv},
	} {
		_, err := ConfigPath(c.flag, environment(map[string]string{ConfigEnv: c.env}), existing())
		if err == nil || !strings.Contains(err.Error(), c.from) {
			t.Errorf("a missing %s file: got %v", c.from, err)
		}
	}
}

func TestLoadConfigRefusesUnknownKeys(t *testing.T) {
	directory := t.TempDir()
	good := filepath.Join(directory, "good.yaml")
	writeFile(t, good, "listen: 0.0.0.0:9123\ntls: false\ntoken_file: /etc/servtuner/token\n")
	config, err := LoadConfig(good)
	if err != nil || config != (Config{Listen: "0.0.0.0:9123", TLS: "false", TokenFile: "/etc/servtuner/token"}) {
		t.Fatalf("got %+v, %v", config, err)
	}
	typo := filepath.Join(directory, "typo.yaml")
	writeFile(t, typo, "tl: false\n")
	if _, err := LoadConfig(typo); err == nil || !strings.Contains(err.Error(), "tl") {
		t.Errorf("a typo must be refused, naming the key: %v", err)
	}
}

func TestCredentialsDefaultToSystemdsDirectory(t *testing.T) {
	env := environment(map[string]string{CredentialsDirectoryEnv: "/run/credentials/servtuner-launcher.service"})
	token := "/run/credentials/servtuner-launcher.service/" + TokenCredential
	key := "/run/credentials/servtuner-launcher.service/" + APIKeyCredential
	if got := (Config{}).WithCredentialDefaults(env, existing(token)); got.TokenFile != token || got.APIKeyFile != "" {
		t.Errorf("no API key credential: got %+v", got)
	}
	if got := (Config{}).WithCredentialDefaults(env, existing(token, key)); got.APIKeyFile != key {
		t.Errorf("the API key credential: got %+v", got)
	}
	set := Config{TokenFile: "/etc/servtuner/token"}
	if got := set.WithCredentialDefaults(env, existing(token)); got.TokenFile != "/etc/servtuner/token" {
		t.Errorf("a configured file wins: got %+v", got)
	}
	if got := (Config{}).WithCredentialDefaults(environment(nil), existing(token)); got.TokenFile != "" {
		t.Errorf("no systemd, no default: got %+v", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
