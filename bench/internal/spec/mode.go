package spec

// TransportOf returns how a target's, upstream's or Prometheus's tls block
// secures its hop, as environment.json records it. It reads the block through
// DecodeTLS, so a block that carries settings it forbids is an error rather
// than being silently reduced.
func TransportOf(tls Tls) (HopTransport, error) {
	decoded, err := DecodeTLS(tls)
	if err != nil {
		return HopTransport{}, err
	}
	return decoded.Transport(), nil
}
