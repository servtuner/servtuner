package loadgen

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

// Error classes written to the request record's error column.
const (
	ErrorTimeout           = "timeout"
	ErrorConnectionRefused = "connection_refused"
	ErrorTLS               = "tls"
	ErrorDNS               = "dns"
	ErrorConnection        = "connection_error"
	ErrorStream            = "stream_error"
	ErrorTruncated         = "truncated"
	// ErrorAbandoned marks a request that was sent and then cut off because the
	// drain time ran out at the end of the run or after a cancel.
	ErrorAbandoned = "abandoned"
)

// httpErrorClass is the class of a response with status 400 or above.
func httpErrorClass(status int) string {
	return fmt.Sprintf("http_%d", status)
}

// classifyTransportError names why a request or its body read failed. A
// timeout wins over everything else because a handshake that times out is a
// timeout, not a TLS fault.
func classifyTransportError(err error) string {
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return ErrorTimeout
	case errors.As(err, &dnsErr):
		return ErrorDNS
	case errors.As(err, &netErr) && netErr.Timeout():
		return ErrorTimeout
	case isTLSError(err):
		return ErrorTLS
	case errors.Is(err, syscall.ECONNREFUSED):
		return ErrorConnectionRefused
	default:
		return ErrorConnection
	}
}

func isTLSError(err error) bool {
	var (
		verification *tls.CertificateVerificationError
		alert        tls.AlertError
		recordHeader tls.RecordHeaderError
		unknownAuth  x509.UnknownAuthorityError
		hostname     x509.HostnameError
		invalidCert  x509.CertificateInvalidError
		echRejection *tls.ECHRejectionError
	)
	return errors.As(err, &verification) || errors.As(err, &alert) || errors.As(err, &recordHeader) ||
		errors.As(err, &unknownAuth) || errors.As(err, &hostname) || errors.As(err, &invalidCert) ||
		errors.As(err, &echRejection) || isRemoteTLSAlert(err)
}

// isRemoteTLSAlert recognises an alert the server sent, for example the refusal
// of a client certificate. With TLS 1.3 the client finishes its handshake
// before the server judges its certificate, so the alert arrives as the error
// of the first read, wrapped in a net.OpError whose cause is crypto/tls's
// unexported alert type; the exported tls.AlertError does not match it.
func isRemoteTLSAlert(err error) bool {
	var opErr *net.OpError
	return errors.As(err, &opErr) && opErr.Op == "remote error" && strings.HasPrefix(opErr.Err.Error(), "tls: ")
}
