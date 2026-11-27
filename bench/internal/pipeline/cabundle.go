package pipeline

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// systemBundleFallbacks are the usual locations of the system CA bundle, tried
// when SSL_CERT_FILE is not set.
var systemBundleFallbacks = []string{
	"/etc/ssl/certs/ca-certificates.crt", // Debian, Ubuntu, Alpine
	"/etc/pki/tls/certs/ca-bundle.crt",   // Fedora, RHEL
	"/etc/ssl/cert.pem",                  // Alpine, macOS
}

// writeCombinedCABundle writes the system trust bundle followed by the private
// CA into a new file under dir and returns its path. A child process with no CA
// option (inference-perf) gets it as SSL_CERT_FILE, so it still verifies public
// endpoints as well as the private one (bench/CLAUDE.md, "Constraints").
func writeCombinedCABundle(dir, privateCAFile, systemBundleFile string, getenv func(string) string) (string, error) {
	systemFile := systemBundleFile
	if systemFile == "" {
		systemFile = getenv("SSL_CERT_FILE")
	}
	var system []byte
	var lookupErrors []error
	candidates := []string{systemFile}
	if systemFile == "" {
		candidates = systemBundleFallbacks
	}
	for _, candidate := range candidates {
		content, err := os.ReadFile(candidate)
		if err == nil {
			system = content
			break
		}
		lookupErrors = append(lookupErrors, err)
	}
	if system == nil {
		return "", fmt.Errorf("find the system CA bundle to combine with the private CA: %w", errors.Join(lookupErrors...))
	}
	private, err := os.ReadFile(privateCAFile)
	if err != nil {
		return "", fmt.Errorf("read private CA bundle: %w", err)
	}
	combined := append(append(append([]byte{}, system...), '\n'), private...)
	location := filepath.Join(dir, "ca-bundle.pem")
	if err := os.WriteFile(location, combined, 0o600); err != nil {
		return "", fmt.Errorf("write combined CA bundle: %w", err)
	}
	return location, nil
}
