package spec

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// DecodedTLS is a tls block read through its mode discriminator. Mode is
// always set; Mtls is set only for mtls and Server only for tls. A platform
// block has nothing beyond its mode.
type DecodedTLS struct {
	Mode   TransportMode
	Mtls   *TlsMtls
	Server *TlsServer
}

// DecodeTLS turns the Tls value of a target, upstream or Prometheus config
// into its typed variant. go-jsonschema generates the oneOf as interface{},
// so this is the one place that reads the mode discriminator.
//
// It is safe on its own: the decode must be lossless, so a platform block
// carrying a secretRef, a tls block carrying a client certificate, or any
// variant with a key the variant does not declare, is an error rather than
// silently dropped. Documents should still come from ParseRunSpec.
func DecodeTLS(tls Tls) (DecodedTLS, error) {
	encoded, err := json.Marshal(tls)
	if err != nil {
		return DecodedTLS{}, fmt.Errorf("encode tls: %w", err)
	}
	var discriminator struct {
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(encoded, &discriminator); err != nil {
		return DecodedTLS{}, fmt.Errorf("read tls mode of %s: %w", encoded, err)
	}
	decoded := DecodedTLS{Mode: TransportMode(discriminator.Mode)}
	var variant any
	switch decoded.Mode {
	case TransportModeMtls:
		decoded.Mtls = new(TlsMtls)
		variant = decoded.Mtls
	case TransportModePlatform:
		variant = new(TlsPlatform)
	case TransportModeTls:
		decoded.Server = new(TlsServer)
		variant = decoded.Server
	default:
		return DecodedTLS{}, fmt.Errorf("unknown tls mode %q in %s", discriminator.Mode, encoded)
	}
	if err := decodeLossless(encoded, variant); err != nil {
		return DecodedTLS{}, fmt.Errorf("decode %s tls: %w", decoded.Mode, err)
	}
	return decoded, nil
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
