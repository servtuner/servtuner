package main

import (
	"crypto/tls"
	"net/http"
	"strings"
	"testing"
)

func mutualConfig(pki testPKI) Config {
	cfg := platformConfig()
	cfg.TLSMode = tlsModeMutual
	cfg.CertFile = pki.serverCertFile
	cfg.KeyFile = pki.serverKeyFile
	cfg.ClientCAFile = pki.caFile
	return cfg
}

func TestMutualTLSAcceptsClientCertificate(t *testing.T) {
	pki := newTestPKI(t)
	address := startMock(t, mutualConfig(pki))
	client := newClient(&tls.Config{
		RootCAs:      pki.caPool,
		Certificates: []tls.Certificate{pki.clientCertificate},
		MinVersion:   tls.VersionTLS12,
	})
	response, err := client.Post("https://"+address+chatCompletionsPath, "application/json", strings.NewReader(`{"messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestMutualTLSRefusesMissingClientCertificate(t *testing.T) {
	pki := newTestPKI(t)
	address := startMock(t, mutualConfig(pki))
	client := newClient(&tls.Config{RootCAs: pki.caPool, MinVersion: tls.VersionTLS12})
	response, err := client.Get("https://" + address + "/healthz")
	if err == nil {
		response.Body.Close()
		t.Fatal("expected the handshake to be refused without a client certificate")
	}
}

func TestMutualTLSRefusesPlainHTTP(t *testing.T) {
	pki := newTestPKI(t)
	address := startMock(t, mutualConfig(pki))
	response, err := newClient(nil).Get("http://" + address + "/healthz")
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Fatal("plain HTTP must not be served in mtls mode")
		}
	}
}

func TestPlatformModeServesPlainHTTP(t *testing.T) {
	address := startMock(t, platformConfig())
	response, err := newClient(nil).Get("http://" + address + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
}

func TestMutualTLSConfigFailsOnUnreadableFiles(t *testing.T) {
	cfg := defaultConfig()
	cfg.CertFile, cfg.KeyFile, cfg.ClientCAFile = "missing.crt", "missing.key", "missing-ca.crt"
	if _, err := mutualTLSConfig(cfg); err == nil {
		t.Fatal("expected an error for missing certificate files")
	}
}

// metricsListenerConfig asks for a separate metrics listener (the address itself is chosen by the test helper).
func metricsListenerConfig(cfg Config) Config {
	cfg.MetricsListenAddress = "127.0.0.1:0"
	return cfg
}

func TestMetricsListenerIsPlainAndMainListenerRefusesMetricsInMutualTLS(t *testing.T) {
	pki := newTestPKI(t)
	address, metricsAddress := startMockWithMetrics(t, metricsListenerConfig(mutualConfig(pki)))

	response, err := newClient(nil).Get("http://" + metricsAddress + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metrics listener: status %d", response.StatusCode)
	}

	withCertificate := newClient(&tls.Config{RootCAs: pki.caPool, Certificates: []tls.Certificate{pki.clientCertificate}, MinVersion: tls.VersionTLS12})
	mainMetrics, err := withCertificate.Get("https://" + address + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer mainMetrics.Body.Close()
	if mainMetrics.StatusCode != http.StatusNotFound {
		t.Fatalf("/metrics on the main listener: status %d, want 404", mainMetrics.StatusCode)
	}

	withoutCertificate := newClient(&tls.Config{RootCAs: pki.caPool, MinVersion: tls.VersionTLS12})
	if response, err := withoutCertificate.Get("https://" + address + "/healthz"); err == nil {
		response.Body.Close()
		t.Fatal("the main listener must still refuse a client without a certificate")
	}
}

func TestMetricsListenerServesOnlyMetricsInPlatformMode(t *testing.T) {
	address, metricsAddress := startMockWithMetrics(t, metricsListenerConfig(platformConfig()))
	get := func(url string) int {
		response, err := newClient(nil).Get(url)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		return response.StatusCode
	}
	if status := get("http://" + metricsAddress + "/metrics"); status != http.StatusOK {
		t.Errorf("metrics listener /metrics: status %d", status)
	}
	if status := get("http://" + metricsAddress + "/healthz"); status != http.StatusNotFound {
		t.Errorf("metrics listener /healthz: status %d, want 404", status)
	}
	if status := get("http://" + address + "/metrics"); status != http.StatusNotFound {
		t.Errorf("main listener /metrics: status %d, want 404", status)
	}
	if status := get("http://" + address + "/healthz"); status != http.StatusOK {
		t.Errorf("main listener /healthz: status %d", status)
	}
}
