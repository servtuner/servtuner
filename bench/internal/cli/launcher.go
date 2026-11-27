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
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// The launcher's defaults when neither a flag nor the config file sets them.
var launcherDefaults = launcher.Config{Listen: ":9123", VLLM: "vllm", LogDir: "servtuner-launcher"}

func newLauncherCommand(streams streams) *cobra.Command {
	var configPath string
	var flags launcher.Config // only the flags' storage: launcherConfig reads them by name
	command := &cobra.Command{
		Use:   "launcher [--config FILE] [--listen ADDR --token-file FILE --tls true|false [--client-auth required|optional|none]]",
		Short: "Run vLLM for servtuner's tuning loop on a host outside Kubernetes",
		Long: "Serves the launcher API: servtuner's deployer starts, stops and inspects `vllm serve` " +
			"here with the flags a tuning session chooses (\"Hosts outside Kubernetes\"). " +
			"Settings come from flags, over a YAML config file (--config, $" + launcher.ConfigEnv + ", " +
			"$XDG_CONFIG_HOME/servtuner/launcher.yaml, then " + launcher.SystemConfigPath + "; deploy/launcher/README.md). " +
			"Every request needs the bearer token in the token file. vLLM's own API key, if any, is read from " +
			"the API key file and given as VLLM_API_KEY, never sent over the network. Under systemd, the files " +
			"default to the unit's LoadCredential= " + launcher.TokenCredential + " and " + launcher.APIKeyCredential +
			". --tls is required and never inferred: false serves plain HTTP; true serves the certificate and " +
			"key files, and --client-auth says whether clients must present a certificate (required), may " +
			"(optional: one that is sent is verified) or need not (none).",
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
	set.StringVar(&flags.TLS, "tls", "", "serve over TLS: true or false; required (tls)")
	set.StringVar(&flags.ClientAuth, "client-auth", "", "client certificates with --tls true: required, optional or none (client_auth)")
	set.StringVar(&flags.TLSCertFile, "tls-cert-file", "", "server certificate, with --tls true (tls_cert_file)")
	set.StringVar(&flags.TLSKeyFile, "tls-key-file", "", "server key, with --tls true (tls_key_file)")
	set.StringVar(&flags.TLSClientCAFile, "tls-client-ca-file", "", "CA that signs client certificates, with --client-auth required or optional (tls_client_ca_file)")
	return command
}

// launcherConfig layers the settings: a flag given on the command line, else the config file, else
// the default; then systemd's credentials for files still unset. The token and tls are required.
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
		"vllm": &config.VLLM, "log-dir": &config.LogDir, "tls": &config.TLS, "client-auth": &config.ClientAuth,
		"tls-cert-file": &config.TLSCertFile, "tls-key-file": &config.TLSKeyFile, "tls-client-ca-file": &config.TLSClientCAFile,
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
	if config.TLS == "" {
		return config, errors.New("no tls setting: set tls in the config or pass --tls (true or false); it is never inferred")
	}
	return config, nil
}

// launcherTLS is the listener's TLS: none with tls false; with tls true the server certificate and
// the client-certificate policy.
func launcherTLS(config launcher.Config) (*tls.Config, error) {
	switch config.TLS {
	case "false":
		if config.TLSCertFile != "" || config.TLSKeyFile != "" || config.TLSClientCAFile != "" || config.ClientAuth != "" {
			return nil, errors.New("tls false serves plain HTTP: drop client_auth and the TLS certificate, key and client CA files")
		}
		return nil, nil
	case "true":
		if config.ClientAuth == "" {
			return nil, errors.New("tls true needs client_auth (--client-auth): required, optional or none")
		}
		clientAuth, err := tlsconfig.ParseClientAuth(config.ClientAuth)
		if err != nil {
			return nil, err
		}
		return tlsconfig.ServerConfig(tlsconfig.Server{
			CertFile: config.TLSCertFile, KeyFile: config.TLSKeyFile, ClientAuth: clientAuth, ClientCAFile: config.TLSClientCAFile,
		})
	case "platform":
		return nil, errors.New("tls_mode platform was replaced in servtuner 0.2: set tls false")
	default:
		return nil, fmt.Errorf("tls %q: want true or false", config.TLS)
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
