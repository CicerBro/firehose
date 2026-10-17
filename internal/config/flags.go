package config

import (
	"fmt"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"
)

// option binds one flag name (and its FIREHOSE_ env var) to a Config field.
type option struct {
	name  string
	usage string
	val   interface {
		String() string
		Set(string) error
	}
}

func (o option) env() string {
	return EnvPrefix + strings.ToUpper(strings.ReplaceAll(o.name, "-", "_"))
}

func (c *Config) options() []option {
	return []option{
		{"listen", "HTTP listen address", (*stringVal)(&c.Listen)},
		{"public-url", "UI origin, e.g. https://speed.example.com (required unless --dev)", (*stringVal)(&c.PublicURL)},
		{"test-hosts", "comma-separated test host origins; empty = single-origin mode", (*listVal)(&c.TestHosts)},
		{"api-token", "bearer token for the CLI; empty disables bearer access", (*secretVal)(&c.APIToken)},
		{"auth-header", "header set by the auth proxy", (*stringVal)(&c.AuthHeader)},
		{"trusted-proxies", "comma-separated CIDRs whose X-Forwarded-For is trusted", (*prefixesVal)(&c.TrustedProxies)},
		{"server-name", "server name shown in the UI", (*stringVal)(&c.ServerName)},
		{"max-active-sessions", "concurrent tests (newest wins)", (*intVal)(&c.MaxActiveSessions)},
		{"max-connections", "max streams per session per direction", (*intVal)(&c.MaxConnections)},
		{"max-request-duration", "hard deadline per down/up request", (*durationVal)(&c.MaxRequestDuration)},
		{"max-download-bytes", "max bytes per download request", (*sizeVal)(&c.MaxDownloadBytes)},
		{"max-upload-bytes", "max body per upload request", (*sizeVal)(&c.MaxUploadBytes)},
		{"session-ttl", "idle session expiry", (*durationVal)(&c.SessionTTL)},
		{"session-max-age", "hard session expiry", (*durationVal)(&c.SessionMaxAge)},
		{"rtc-enabled", "enable the WebRTC packet-loss test", (*boolVal)(&c.RTCEnabled)},
		{"rtc-udp-port", "single UDP port for WebRTC", (*intVal)(&c.RTCUDPPort)},
		{"rtc-public-ips", "comma-separated public IPs advertised as ICE host candidates", (*listVal)(&c.RTCPublicIPs)},
		{"log-level", "debug, info, warn or error", (*levelVal)(&c.LogLevel)},
		{"log-format", "json or text", (*stringVal)(&c.LogFormat)},
		{"metrics-listen", "address for Prometheus /metrics; empty = off", (*stringVal)(&c.MetricsListen)},
		{"dev", "development mode: NO AUTH CHECK, debug query overrides", (*boolVal)(&c.Dev)},
	}
}

type stringVal string

func (v *stringVal) String() string     { return string(*v) }
func (v *stringVal) Set(s string) error { *v = stringVal(strings.TrimSpace(s)); return nil }

// secretVal never prints its value (usage output).
type secretVal string

func (v *secretVal) String() string     { return "" }
func (v *secretVal) Set(s string) error { *v = secretVal(strings.TrimSpace(s)); return nil }

type intVal int

func (v *intVal) String() string { return strconv.Itoa(int(*v)) }
func (v *intVal) Set(s string) error {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid integer %q", s)
	}
	*v = intVal(n)
	return nil
}

type boolVal bool

func (v *boolVal) String() string   { return strconv.FormatBool(bool(*v)) }
func (v *boolVal) IsBoolFlag() bool { return true }
func (v *boolVal) Set(s string) error {
	b, err := strconv.ParseBool(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid boolean %q", s)
	}
	*v = boolVal(b)
	return nil
}

type durationVal time.Duration

func (v *durationVal) String() string { return time.Duration(*v).String() }
func (v *durationVal) Set(s string) error {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("invalid duration %q", s)
	}
	*v = durationVal(d)
	return nil
}

type sizeVal int64

func (v *sizeVal) String() string { return FormatSize(int64(*v)) }
func (v *sizeVal) Set(s string) error {
	n, err := ParseSize(s)
	if err != nil {
		return err
	}
	*v = sizeVal(n)
	return nil
}

type listVal []string

func (v *listVal) String() string { return strings.Join(*v, ",") }
func (v *listVal) Set(s string) error {
	*v = splitList(s)
	return nil
}

type prefixesVal []netip.Prefix

func (v *prefixesVal) String() string {
	parts := make([]string, len(*v))
	for i, p := range *v {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

func (v *prefixesVal) Set(s string) error {
	var out []netip.Prefix
	for _, part := range splitList(s) {
		p, err := netip.ParsePrefix(part)
		if err != nil {
			// Accept a bare IP as a single-host prefix.
			a, aerr := netip.ParseAddr(part)
			if aerr != nil {
				return fmt.Errorf("invalid CIDR %q", part)
			}
			a = a.Unmap()
			p = netip.PrefixFrom(a, a.BitLen())
		}
		out = append(out, p.Masked())
	}
	*v = out
	return nil
}

type levelVal slog.Level

func (v *levelVal) String() string { return strings.ToLower(slog.Level(*v).String()) }
func (v *levelVal) Set(s string) error {
	var l slog.Level
	if err := l.UnmarshalText([]byte(strings.TrimSpace(s))); err != nil {
		return fmt.Errorf("invalid log level %q", s)
	}
	*v = levelVal(l)
	return nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
