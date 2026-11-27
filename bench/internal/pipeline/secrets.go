package pipeline

import (
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/servtuner/servtuner/bench/internal/spec"
	"github.com/servtuner/servtuner/bench/internal/tlsconfig"
)

// DefaultSecretsDir is where the Secrets a RunSpec names are mounted: each
// Secret at <dir>/<secret name>, so a key is the file <dir>/<name>/<key>. A
// kubernetes.io/tls Secret therefore appears as <dir>/<name>/tls.crt, tls.key
// and ca.crt. The RunSpec holds names only, never values (root CLAUDE.md, rule 4).
const DefaultSecretsDir = "/etc/servtuner/secrets"

// File names inside a kubernetes.io/tls Secret.
const (
	tlsCertKey = "tls.crt"
	tlsKeyKey  = "tls.key"
	tlsCAKey   = "ca.crt"
)

// secrets resolves RunSpec secret references to the files mounted under dir.
// No method returns or logs a secret's value in an error.
type secrets struct {
	dir string
}

// path is the file of one Secret key. The name and key come from the RunSpec,
// so they must not climb out of the mount.
func (s secrets) path(secretName, key string) (string, error) {
	for _, part := range []string{secretName, key} {
		if part == "" || part == "." || part == ".." || filepath.Base(part) != part || strings.ContainsRune(part, filepath.Separator) {
			return "", fmt.Errorf("secret reference %q/%q is not a plain secret name and key", secretName, key)
		}
	}
	return filepath.Join(s.dir, secretName, key), nil
}

// read returns the content of one key, minus one trailing newline.
func (s secrets) read(secretName, key string) (string, error) {
	location, err := s.path(secretName, key)
	if err != nil {
		return "", err
	}
	content, err := os.ReadFile(location)
	if err != nil {
		return "", fmt.Errorf("%w: read secret %s key %s from %s: %w", errSecretUnavailable, secretName, key, s.dir, unwrapPath(err))
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(content), "\n"), "\r")
	if value == "" {
		return "", fmt.Errorf("secret %s key %s is empty", secretName, key)
	}
	return value, nil
}

// unwrapPath keeps the cause of a file error without repeating the path.
func unwrapPath(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr.Err
	}
	return err
}

// hop builds the tlsconfig.Hop of a connection from its declared tls block.
// The mode is the RunSpec's (never inferred from the URL); an mtls hop needs
// tls.crt and tls.key in the named Secret, and picks up ca.crt when there is
// one; a tls hop reads only ca.crt, from its caSecretRef, which must then
// hold it.
func (s secrets) hop(name string, tls spec.Tls, url string) (tlsconfig.Hop, error) {
	decoded, err := spec.DecodeTLS(tls)
	if err != nil {
		return tlsconfig.Hop{}, fmt.Errorf("%s.tls: %w", name, err)
	}
	hop := tlsconfig.Hop{Name: name, Mode: decoded.Mode, URL: url}
	switch decoded.Mode {
	case spec.TransportModePlatform:
		return hop, nil
	case spec.TransportModeTls:
		if decoded.Server.CaSecretRef == nil {
			return hop, nil
		}
		if hop.CAFile, err = s.existing(decoded.Server.CaSecretRef.Name, tlsCAKey); err != nil {
			return tlsconfig.Hop{}, fmt.Errorf("%s hop: %w: %w", name, errSecretUnavailable, err)
		}
		return hop, nil
	}
	secretName := decoded.Mtls.SecretRef.Name
	var problems []error
	if hop.CertFile, err = s.existing(secretName, tlsCertKey); err != nil {
		problems = append(problems, err)
	}
	if hop.KeyFile, err = s.existing(secretName, tlsKeyKey); err != nil {
		problems = append(problems, err)
	}
	if err := errors.Join(problems...); err != nil {
		return tlsconfig.Hop{}, fmt.Errorf("%s hop: %w: %w", name, errSecretUnavailable, err)
	}
	// ca.crt is optional: a public CA signs the server, or the pod's system
	// bundle already holds the private one.
	if caFile, err := s.existing(secretName, tlsCAKey); err == nil {
		hop.CAFile = caFile
	}
	return hop, nil
}

// existing returns the path of a key that is mounted as a readable file.
func (s secrets) existing(secretName, key string) (string, error) {
	location, err := s.path(secretName, key)
	if err != nil {
		return "", err
	}
	file, err := os.Open(location)
	if err != nil {
		return "", fmt.Errorf("secret %s has no readable %s under %s: %w", secretName, key, s.dir, unwrapPath(err))
	}
	return location, file.Close()
}

// bearerToken resolves target.auth. It returns "" when the RunSpec has none.
func (s secrets) bearerToken(auth *spec.TargetAuth) (string, error) {
	if auth == nil {
		return "", nil
	}
	token, err := s.read(auth.SecretRef.Name, auth.SecretRef.Key)
	if err != nil {
		return "", fmt.Errorf("target.auth: %w", err)
	}
	if strings.IndexFunc(token, func(r rune) bool { return r < ' ' || r == 0x7f }) >= 0 {
		return "", fmt.Errorf("target.auth: secret %s key %s contains control characters", auth.SecretRef.Name, auth.SecretRef.Key)
	}
	return token, nil
}

// authorization is the Authorization header value the snapshot sends to the
// target's own endpoints. The admin credential wins when the RunSpec names one,
// because a config API such as Bifrost's /api/config sits behind it; otherwise
// it is target.auth as a bearer token. The value is never logged.
func (s secrets) authorization(target spec.Target) (string, error) {
	if target.Admin != nil {
		return s.adminAuthorization(target.Admin.SecretRef)
	}
	token, err := s.bearerToken(target.Auth)
	if err != nil || token == "" {
		return "", err
	}
	return "Bearer " + token, nil
}

// adminAuthorization is a bearer token (tokenKey) or HTTP basic credentials
// (usernameKey and passwordKey).
func (s secrets) adminAuthorization(ref spec.AdminSecretRef) (string, error) {
	switch {
	case ref.TokenKey != nil:
		token, err := s.read(ref.Name, *ref.TokenKey)
		if err != nil {
			return "", fmt.Errorf("target.admin: %w", err)
		}
		return "Bearer " + token, nil
	case ref.UsernameKey != nil && ref.PasswordKey != nil:
		username, err := s.read(ref.Name, *ref.UsernameKey)
		if err != nil {
			return "", fmt.Errorf("target.admin: %w", err)
		}
		password, err := s.read(ref.Name, *ref.PasswordKey)
		if err != nil {
			return "", fmt.Errorf("target.admin: %w", err)
		}
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password)), nil
	default:
		return "", fmt.Errorf("target.admin: secretRef %s names neither tokenKey nor usernameKey with passwordKey", ref.Name)
	}
}
