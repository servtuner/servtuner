// Command openai-mock is an OpenAI-compatible target with configurable
// latency, used to exercise servtuner without GPUs.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if filepath.Base(os.Args[0]) == vllmCommand {
		os.Exit(runVLLM(os.Args[1:], osEnvironment, os.Stdout, os.Stderr, time.Sleep))
	}
	cfg, err := loadConfig(os.Args[1:], osEnvironment, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "openai-mock:", err)
		os.Exit(2)
	}
	if err := runUntilSignal(cfg); err != nil {
		log.Fatalf("openai-mock: %v", err)
	}
}

// runUntilSignal serves until SIGINT or SIGTERM.
func runUntilSignal(cfg Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	return run(ctx, cfg)
}

func run(ctx context.Context, cfg Config) error {
	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddress, err)
	}
	var metricsListener net.Listener
	if cfg.MetricsListenAddress != "" {
		metricsListener, err = net.Listen("tcp", cfg.MetricsListenAddress)
		if err != nil {
			return fmt.Errorf("listen for metrics on %s: %w", cfg.MetricsListenAddress, err)
		}
	}
	return serve(ctx, cfg, listener, metricsListener)
}

// serve runs the mock on an existing listener until ctx ends. metricsListener is nil unless
// cfg.MetricsListenAddress is set; it always serves plain HTTP, whatever the TLS mode. Tests pass listeners on
// port 0.
func serve(ctx context.Context, cfg Config, listener, metricsListener net.Listener) error {
	mainHandler, metricsHandler, m := newHandlers(cfg)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	startMemoryRamp(ctx, cfg.Faults, m, ticker.C)
	servers := []*http.Server{{Handler: mainHandler, ReadHeaderTimeout: 10 * time.Second}}

	switch cfg.TLSMode {
	case tlsModeMutual:
		tlsConfig, err := mutualTLSConfig(cfg)
		if err != nil {
			return err
		}
		listener = tls.NewListener(listener, tlsConfig)
	case tlsModeServer:
		certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return fmt.Errorf("load server certificate %s and key %s: %w", cfg.CertFile, cfg.KeyFile, err)
		}
		listener = tls.NewListener(listener, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{certificate}})
	}

	listeners := []net.Listener{listener}
	if metricsListener != nil {
		servers = append(servers, &http.Server{Handler: metricsHandler, ReadHeaderTimeout: 10 * time.Second})
		listeners = append(listeners, metricsListener)
	}

	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, httpServer := range servers {
			_ = httpServer.Shutdown(shutdownContext)
		}
	}()

	log.Printf("openai-mock serving model %q on %s (tls-mode %s)", cfg.ModelName, listener.Addr(), cfg.TLSMode)
	if metricsListener != nil {
		log.Printf("openai-mock serving /metrics in plain HTTP on %s", metricsListener.Addr())
	}
	results := make(chan error, len(servers))
	for i, httpServer := range servers {
		go func() { results <- httpServer.Serve(listeners[i]) }()
	}
	var firstError error
	for range servers {
		if err := <-results; !errors.Is(err, http.ErrServerClosed) && firstError == nil {
			firstError = fmt.Errorf("serve: %w", err)
			stopAll(servers)
		}
	}
	return firstError
}

// stopAll closes every server immediately, so one failed listener does not leave the other running.
func stopAll(servers []*http.Server) {
	for _, httpServer := range servers {
		_ = httpServer.Close()
	}
}

// mutualTLSConfig requires and verifies client certificates.
func mutualTLSConfig(cfg Config) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("load server certificate %s and key %s: %w", cfg.CertFile, cfg.KeyFile, err)
	}
	caPEM, err := os.ReadFile(cfg.ClientCAFile)
	if err != nil {
		return nil, fmt.Errorf("read client CA %s: %w", cfg.ClientCAFile, err)
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("client CA %s contains no PEM certificates", cfg.ClientCAFile)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{certificate},
		ClientCAs:    clientCAs,
		ClientAuth:   tls.RequireAndVerifyClientCert,
	}, nil
}
