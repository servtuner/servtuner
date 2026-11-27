package cli

import (
	"io"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/launcher"
)

// The systemd units users install and the example config; they must keep naming
// flags and keys the launcher has.
const (
	launcherDeploy     = "../../../deploy/launcher/"
	launcherUnit       = launcherDeploy + "servtuner-launcher.service"
	launcherSystemUnit = launcherDeploy + "servtuner-launcher-system.service"
	launcherConfigFile = launcherDeploy + "launcher.yaml"
)

func TestTheExampleConfigLoads(t *testing.T) {
	config, err := launcher.LoadConfig(launcherConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	if config.TLSMode != "platform" || config.Listen == "" {
		t.Errorf("the example sets the listener and the TLS mode: %+v", config)
	}
}

func TestTheSystemUnitReadsTheSystemConfigAndCredentials(t *testing.T) {
	content, err := os.ReadFile(launcherSystemUnit)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"--config=" + launcher.SystemConfigPath,
		"LoadCredential=" + launcher.TokenCredential + ":",
		"# LoadCredential=" + launcher.APIKeyCredential + ":",
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("%s does not contain %q", launcherSystemUnit, want)
		}
	}
}

func TestTheLauncherUnitUsesTheLaunchersFlags(t *testing.T) {
	for _, unit := range []string{launcherUnit, launcherSystemUnit} {
		checkUnitFlags(t, unit)
	}
}

func checkUnitFlags(t *testing.T, unit string) {
	t.Helper()
	content, err := os.ReadFile(unit)
	if err != nil {
		t.Fatalf("read %s: %v", unit, err)
	}
	execStart := regexp.MustCompile(`(?m)^ExecStart=((?:.*\\\n)*.*)$`).FindStringSubmatch(string(content))
	if execStart == nil {
		t.Fatalf("%s has no ExecStart", unit)
	}
	used := map[string]bool{}
	for _, match := range regexp.MustCompile(`--([a-z-]+)=`).FindAllStringSubmatch(execStart[1], -1) {
		used[match[1]] = true
	}
	if !strings.Contains(execStart[1], "servtuner launcher") {
		t.Errorf("ExecStart does not run `servtuner launcher`: %q", execStart[1])
	}
	command := newLauncherCommand(streams{out: io.Discard, err: io.Discard})
	for name := range used {
		if command.Flags().Lookup(name) == nil {
			t.Errorf("%s passes --%s, which servtuner launcher does not have", unit, name)
		}
	}
	// The token and TLS mode come from flags, or from the config file the unit names.
	if !used["config"] {
		for _, required := range []string{"token-file", "tls-mode"} {
			if !used[required] {
				t.Errorf("%s passes neither --config nor the required --%s", unit, required)
			}
		}
	}
}

func TestLauncherFlagsWinOverTheConfigFile(t *testing.T) {
	directory := t.TempDir()
	path := directory + "/launcher.yaml"
	if err := os.WriteFile(path, []byte("listen: 127.0.0.1:9999\ntls_mode: platform\ntoken_file: /from/file\nvllm: /opt/vllm/bin/vllm\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	command := newLauncherCommand(streams{out: io.Discard, err: io.Discard})
	if err := command.ParseFlags([]string{"--config", path, "--token-file", "/from/flag"}); err != nil {
		t.Fatal(err)
	}
	config, err := launcherConfig(command)
	if err != nil {
		t.Fatal(err)
	}
	if config.TokenFile != "/from/flag" || config.Listen != "127.0.0.1:9999" || config.VLLM != "/opt/vllm/bin/vllm" ||
		config.LogDir != launcherDefaults.LogDir {
		t.Errorf("flag over file over default: got %+v", config)
	}
}

func TestLauncherNeedsATokenAndATLSMode(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", "")
	t.Setenv("SERVTUNER_LAUNCHER_CONFIG", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	command := newLauncherCommand(streams{out: io.Discard, err: io.Discard})
	if err := command.ParseFlags([]string{"--tls-mode", "platform"}); err != nil {
		t.Fatal(err)
	}
	if _, err := launcherConfig(command); err == nil || !strings.Contains(err.Error(), "LoadCredential=launcher-token") {
		t.Errorf("no token: got %v", err)
	}
}
