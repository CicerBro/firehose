package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
)

// Exit codes (BUILD-PLAN §10.5).
const (
	ExitOK          = 0
	ExitUsage       = 1
	ExitUnreachable = 2
	ExitAuth        = 3
	ExitPhaseFailed = 4
	ExitInterrupted = 130
)

// UsageError is a usage or configuration error (exit 1).
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// AuthError means the server rejected the credentials: a 401, or a 3xx
// redirect (usually to the Authelia login because the request did not match
// the bearer-token router). Exit 3.
type AuthError struct {
	Status   int
	Location string // redirect target, if any
	URL      string
}

func (e *AuthError) Error() string {
	if e.Status >= 300 && e.Status < 400 {
		loc := e.Location
		if loc == "" {
			loc = "(no Location header)"
		}
		return fmt.Sprintf("authentication failed: %s redirected (%d) to %s; the request did not match the bearer-token router (missing or wrong --token / FIREHOSE_TOKEN?)", e.URL, e.Status, loc)
	}
	return fmt.Sprintf("authentication failed: %s returned %d; check --token / FIREHOSE_TOKEN (it must equal the server's FIREHOSE_API_TOKEN)", e.URL, e.Status)
}

// UnreachableError means the server could not be reached or the TLS
// handshake failed, or it did not answer like a firehose server. Exit 2.
type UnreachableError struct{ Err error }

func (e *UnreachableError) Error() string { return "server unreachable: " + e.Err.Error() }
func (e *UnreachableError) Unwrap() error { return e.Err }

// PhaseError means the test ran but a phase failed (e.g. every stream
// errored, or the overall timeout hit). Exit 4.
type PhaseError struct {
	Phase string
	Err   error
}

func (e *PhaseError) Error() string { return e.Phase + " failed: " + e.Err.Error() }
func (e *PhaseError) Unwrap() error { return e.Err }

// ErrInterrupted is returned (wrapped with context.Canceled) when the run's
// context is cancelled, e.g. by Ctrl-C. Partial results are returned with it.
var ErrInterrupted = errors.New("interrupted")

// ExitCode maps an error returned by Run (or by option validation) to the
// process exit code of §10.5. nil → 0.
func ExitCode(err error) int {
	var (
		ue *UsageError
		ae *AuthError
		ne *UnreachableError
		pe *PhaseError
	)
	switch {
	case err == nil:
		return ExitOK
	case errors.Is(err, ErrInterrupted), errors.Is(err, context.Canceled):
		return ExitInterrupted
	case errors.As(err, &ue):
		return ExitUsage
	case errors.As(err, &ae):
		return ExitAuth
	case errors.As(err, &pe):
		return ExitPhaseFailed
	case errors.As(err, &ne):
		return ExitUnreachable
	case isNetOrTLS(err):
		return ExitUnreachable
	default:
		return ExitPhaseFailed
	}
}

// isNetOrTLS reports whether err is a dial, DNS or TLS failure.
func isNetOrTLS(err error) bool {
	var (
		opErr   *net.OpError
		dnsErr  *net.DNSError
		recErr  tls.RecordHeaderError
		certErr *tls.CertificateVerificationError
		unkAuth x509.UnknownAuthorityError
		hostErr x509.HostnameError
		invErr  x509.CertificateInvalidError
		alert   tls.AlertError
	)
	return errors.As(err, &opErr) || errors.As(err, &dnsErr) || errors.As(err, &recErr) ||
		errors.As(err, &certErr) || errors.As(err, &unkAuth) || errors.As(err, &hostErr) ||
		errors.As(err, &invErr) || errors.As(err, &alert)
}

// redactURL strips the query (it carries the session ID) for messages.
func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	c.RawQuery = ""
	return c.String()
}
