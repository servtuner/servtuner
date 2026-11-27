package spec

// ModeOf returns the transport mode declared by a target's, upstream's or
// Prometheus's tls block. It reads the mode through DecodeTLS, so a block that
// carries settings its mode forbids is an error rather than being silently
// reduced to its mode.
func ModeOf(tls Tls) (TransportMode, error) {
	decoded, err := DecodeTLS(tls)
	if err != nil {
		return "", err
	}
	return decoded.Mode, nil
}
