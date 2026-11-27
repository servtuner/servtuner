package spec

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// DecodedTLS is a tls block read through its enabled discriminator. ClientCert
// and CA are set only when TLS is enabled and the block names them.
type DecodedTLS struct {
	Enabled    bool
	ClientCert *SecretRef
	CA         *SecretRef
}

// Transport is what environment.json records for the hop: whether it used TLS
// and whether servtuner presented a client certificate.
func (d DecodedTLS) Transport() HopTransport {
	return HopTransport{Tls: d.Enabled, ClientCert: d.ClientCert != nil}
}

// DecodeTLS turns the Tls value of a target, upstream or Prometheus config
// into its typed variant. go-jsonschema generates the oneOf as interface{},
// so this is the one place that reads the enabled discriminator.
//
// It is safe on its own: the decode must be lossless, so a disabled block
// carrying a certificate, or any variant with a key the variant does not
// declare, is an error rather than silently dropped. A servtuner 0.1 block
// (tls.mode) is an error that names its replacement. Documents should still
// come from ParseRunSpec.
func DecodeTLS(tls Tls) (DecodedTLS, error) {
	encoded, err := json.Marshal(tls)
	if err != nil {
		return DecodedTLS{}, fmt.Errorf("encode tls: %w", err)
	}
	var discriminator struct {
		Enabled *bool           `json:"enabled"`
		Mode    json.RawMessage `json:"mode"`
	}
	if err := json.Unmarshal(encoded, &discriminator); err != nil {
		return DecodedTLS{}, fmt.Errorf("read tls.enabled of %s: %w", encoded, err)
	}
	if discriminator.Mode != nil {
		return DecodedTLS{}, legacyTLSError(encoded)
	}
	if discriminator.Enabled == nil {
		return DecodedTLS{}, fmt.Errorf("tls.enabled is required in %s: true for TLS, false for plain HTTP", encoded)
	}
	if !*discriminator.Enabled {
		if err := decodeLossless(encoded, new(TlsDisabled)); err != nil {
			return DecodedTLS{}, fmt.Errorf("decode tls with enabled false (it takes no certificate settings): %w", err)
		}
		return DecodedTLS{}, nil
	}
	var enabled TlsEnabled
	if err := decodeLossless(encoded, &enabled); err != nil {
		return DecodedTLS{}, fmt.Errorf("decode tls with enabled true: %w", err)
	}
	decoded := DecodedTLS{Enabled: true, CA: enabled.CaSecretRef}
	if enabled.ClientCert != nil {
		decoded.ClientCert = &enabled.ClientCert.SecretRef
	}
	return decoded, nil
}

// legacyTLSError explains a servtuner 0.1 tls block in terms of 0.2's.
func legacyTLSError(encoded []byte) error {
	const replaced = "tls.mode was replaced in servtuner 0.2 by tls.enabled and tls.clientCert"
	var legacy map[string]any
	if err := json.Unmarshal(encoded, &legacy); err != nil {
		return fmt.Errorf("%s: %s", replaced, encoded)
	}
	upgraded, ok := upgradeLegacyTLS(legacy)
	if !ok {
		return fmt.Errorf("%s, and tls.mode %v has no equivalent: %s", replaced, legacy["mode"], encoded)
	}
	replacement, err := json.Marshal(upgraded)
	if err != nil {
		return fmt.Errorf("%s: %s", replaced, encoded)
	}
	return fmt.Errorf("%s: write %s instead of %s", replaced, replacement, encoded)
}

// decodeLossless unmarshals into variant and fails if marshalling it back does
// not reproduce the input. The generated UnmarshalJSON methods run their own
// decoder, so json.Decoder.DisallowUnknownFields cannot reach inside them.
func decodeLossless(encoded []byte, variant any) error {
	if err := json.Unmarshal(encoded, variant); err != nil {
		return err
	}
	remarshaled, err := json.Marshal(variant)
	if err != nil {
		return err
	}
	var want, got any
	if err := json.Unmarshal(encoded, &want); err != nil {
		return err
	}
	if err := json.Unmarshal(remarshaled, &got); err != nil {
		return err
	}
	if !reflect.DeepEqual(want, got) {
		return fmt.Errorf("fields outside the variant: %s decodes as %s", encoded, remarshaled)
	}
	return nil
}
