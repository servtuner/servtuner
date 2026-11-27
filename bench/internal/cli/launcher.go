package cli

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/servtuner/servtuner/bench/internal/launcher"
)

// The launcher's defaults when neither a flag nor the config file sets them.
var launcherDefaults = launcher.Config{Listen: ":9123", VLLM: "vllm", LogDir: "servtuner-launcher"}

func newLauncherCommand(streams streams) *cobra.Command {
	var configPath string
	var flags launcher.Config // only the flags' storage: launcherConfig reads them by name
	command := &cobra.Command{
		Use:   "launcher [--config FILE] [--listen ADDR --token-file FILE --tls-mode platform|tls]",
		Short: "Run vLLM for servtuner's tuning loop on a host outside Kubernetes",
		Long: "Serves the launcher API: servtuner's deployer starts, stops and inspects `vllm serve` " +
			"here with the flags a tuning session chooses (\"Hosts outside Kubernetes\"). " +
			"Settings come from flags, over a YAML config file (--config, $" + launcher.ConfigEnv + ", " +
			"$XDG_CONFIG_HOME/servtuner/launcher.yaml, then " + launcher.SystemConfigPath + "; deploy/launcher/README.md). " +
			"Every request needs the bearer token in the token file. vLLM's own API key, if any, is read from " +
			"the API key file and given as VLLM_API_KEY, never sent over the network. Under systemd, the files " +
			"default to the unit's LoadCredential= " + launcher.TokenCredential + " and " + launcher.APIKeyCredential +
			". The TLS mode is required and never inferred: platform serves plain HTTP, tls serves " +
			"the certificate and key files.",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := launcherConfig(command)
			if err != nil {
				return usageError(err)
			}
			token, err := readSecretFile(config.TokenFile, "token file")
			if err != nil {
				return usageError(err)
			}
			apiKey := ""
			if config.APIKeyFile != "" {
				if apiKey, err = readSecretFile(config.APIKeyFile, "API key file"); err != nil {
					return usageError(err)
				}
			}
			tlsConfig, err := launcherTLS(config)
			if err != nil {
				return usageError(err)
			}
			if err := os.MkdirAll(config.LogDir, 0o700); err != nil {
				return failure(fmt.Errorf("create %s: %w", config.LogDir, err))
			}
			supervisor := launcher.New(launcher.Options{
				VLLM: config.VLLM, LogDir: config.LogDir, Token: token, APIKey: apiKey, BaseEnv: os.Environ(),
			})
			return serveLauncher(streams, config.Listen, tlsConfig, supervisor)
		},
	}
	set := command.Flags()
	set.StringVar(&configPath, "config", "", "YAML config file (default: $"+launcher.ConfigEnv+
		", $XDG_CONFIG_HOME/servtuner/launcher.yaml, "+launcher.SystemConfigPath+")")
	set.StringVar(&flags.Listen, "listen", launcherDefaults.Listen, "address to serve the launcher API on (listen)")
	set.StringVar(&flags.TokenFile, "token-file", "", "file holding the bearer token every request must carry (token_file)")
	set.StringVar(&flags.APIKeyFile, "api-key-file", "",
		"file holding vLLM's --api-key value, given to vLLM as VLLM_API_KEY (api_key_file)")
	set.StringVar(&flags.VLLM, "vllm", launcherDefaults.VLLM, "the vllm executable (vllm)")
	set.StringVar(&flags.LogDir, "log-dir", launcherDefaults.LogDir, "directory for vllm.log (log_dir)")
	set.StringVar(&flags.TLSMode, "tls-mode", "", "platform (plain HTTP) or tls; required (tls_mode)")
	set.StringVar(&flags.TLSCertFile, "tls-cert-file", "", "server certificate, tls mode (tls_cert_file)")
	set.StringVar(&flags.TLSKeyFile, "tls-key-file", "", "server key, tls mode (tls_key_file)")
	return command
}

// launcherConfig layers the settings: a flag given on the command line, else the config file, else
// the default; then systemd's credentials for files still unset. The token and TLS mode are required.
func launcherConfig(command *cobra.Command) (launcher.Config, error) {
	set := command.Flags()
	configPath, _ := set.GetString("config")
	path, err := launcher.ConfigPath(configPath, os.Getenv, launcher.FileExists)
	if err != nil {
		return launcher.Config{}, err
	}
	config, err := launcher.LoadConfig(path)
	if err != nil {
		return launcher.Config{}, err
	}
	for flag, value := range map[string]*string{
		"listen": &config.Listen, "token-file": &config.TokenFile, "api-key-file": &config.APIKeyFile,
		"vllm": &config.VLLM, "log-dir": &config.LogDir, "tls-mode": &config.TLSMode,
		"tls-cert-file": &config.TLSCertFile, "tls-key-file": &config.TLSKeyFile,
	} {
		// A flag's own default is the fallback, so the file wins over it but not over a typed flag.
		if set.Changed(flag) || *value == "" {
			*value, _ = set.GetString(flag)
		}
	}
	config = config.WithCredentialDefaults(os.Getenv, launcher.FileExists)
	if config.TokenFile == "" {
		return config, errors.New("no token file: set token_file in the config, pass --token-file, " +
			"or give the unit LoadCredential=" + launcher.TokenCredential)
	}
	if config.TLSMode == "" {
		return config, errors.New("no TLS mode: set tls_mode in the config or pass --tls-mode (platform or tls)")
	}
	return config, nil
}

// launcherTLS is the listener's TLS: none in platform mode, the server certificate in tls mode.
func launcherTLS(config launcher.Config) (*tls.Config, error) {
	switch config.TLSMode {
	case "platform":
		if config.TLSCertFile != "" || config.TLSKeyFile != "" {
			return nil, errors.New("tls mode platform serves plain HTTP: drop the TLS certificate and key files")
		}
		return nil, nil
	case "tls":
		certificate, err := tls.LoadX509KeyPair(config.TLSCertFile, config.TLSKeyFile)
		if err != nil {
			return nil, fmt.Errorf("tls mode tls: %w", err)
		}
		return &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}}, nil
	default:
		return nil, fmt.Errorf("tls mode %q: want platform or tls", config.TLSMode)
	}
}

func readSecretFile(path, flag string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("%s: %w", flag, err)
	}
	value := strings.TrimSpace(string(content))
	if value == "" {
		return "", fmt.Errorf("%s %s is empty", flag, path)
	}
	return value, nil
}

// serveLauncher serves until SIGINT or SIGTERM, then stops vLLM so the GPU is freed.
func serveLauncher(streams streams, listen string, tlsConfig *tls.Config, supervisor *launcher.Launcher) error {
	listener, err := net.Listen("tcp", listen)
	if err != nil {
		return failure(fmt.Errorf("listen on %s: %w", listen, err))
	}
	if tlsConfig != nil {
		listener = tls.NewListener(listener, tlsConfig)
	}
	server := &http.Server{Handler: supervisor.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		supervisor.Stop()
		_ = server.Close()
	}()
	fmt.Fprintf(streams.err, "servtuner launcher: serving on %s\n", listener.Addr())
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return failure(err)
	}
	return nil
}
