package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// StreamBufferSize is the per-connection read and write buffer size.
const StreamBufferSize = 256 << 10

const (
	dialTimeout         = 10 * time.Second
	tlsHandshakeTimeout = 10 * time.Second
)

// netConfig is the network setup shared by every transport of a run.
type netConfig struct {
	family    int // 0 (any), 4 or 6
	tlsConfig *tls.Config
}

func newNetConfig(o *Options) (netConfig, error) {
	tc := &tls.Config{
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	if o.Insecure {
		tc.InsecureSkipVerify = true // --insecure; the CLI prints a warning
	}
	if o.CAFile != "" {
		pem, err := os.ReadFile(o.CAFile)
		if err != nil {
			return netConfig{}, &UsageError{Msg: fmt.Sprintf("--ca-file: %v", err)}
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return netConfig{}, &UsageError{Msg: fmt.Sprintf("--ca-file %s: no PEM certificates found", o.CAFile)}
		}
		tc.RootCAs = pool
	}
	return netConfig{family: o.IPFamily, tlsConfig: tc}, nil
}

// network returns the dial network for the configured family.
func (nc netConfig) network() string {
	switch nc.family {
	case 4:
		return "tcp4"
	case 6:
		return "tcp6"
	}
	return "tcp"
}

func (nc netConfig) dialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := &net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}
	nw := nc.network()
	return func(ctx context.Context, _, addr string) (net.Conn, error) {
		return d.DialContext(ctx, nw, addr)
	}
}

// newStreamTransport returns a transport for exactly one test stream
// (§10.3): HTTP/1.1 only (a non-nil empty TLSNextProto disables h2 and
// ForceAttemptHTTP2 is off), no compression, at most one connection, 256 KiB
// buffers and no proxy, so N streams are N real TCP connections.
func (nc netConfig) newStreamTransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil, // never measure through a proxy
		DialContext:         nc.dialContext(),
		TLSClientConfig:     nc.tlsConfig.Clone(),
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
		MaxConnsPerHost:     1,
		MaxIdleConns:        1,
		MaxIdleConnsPerHost: 1,
		IdleConnTimeout:     90 * time.Second,
		ReadBufferSize:      StreamBufferSize,
		WriteBufferSize:     StreamBufferSize,
	}
}

// newAPITransport is the transport for /api/config, /api/ip, /api/session
// and the WebSocket upgrade. Same network and TLS settings, HTTP/1.1 (the
// WebSocket handshake requires it), small buffers.
func (nc netConfig) newAPITransport() *http.Transport {
	return &http.Transport{
		Proxy:               nil,
		DialContext:         nc.dialContext(),
		TLSClientConfig:     nc.tlsConfig.Clone(),
		TLSHandshakeTimeout: tlsHandshakeTimeout,
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		ForceAttemptHTTP2:   false,
		DisableCompression:  true,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
}

// newHTTPClient wraps rt in a client that never follows redirects: a 3xx
// is returned as is and reported as an AuthError (§10.3).
func newHTTPClient(rt http.RoundTripper) *http.Client {
	return &http.Client{
		Transport: rt,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}
