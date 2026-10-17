// Package config holds the server configuration: defaults, environment
// variables, command-line flags (flags override env) and validation.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvPrefix is the prefix of every environment variable.
const EnvPrefix = "FIREHOSE_"

// SingleOriginMaxConnections is the per-host connection cap the client applies
// in single-origin mode (no TEST_HOSTS).
const SingleOriginMaxConnections = 6

// ConnectionsPerHost is the number of connections the client puts on one test
// host (hostsNeeded = ceil(n / ConnectionsPerHost)).
const ConnectionsPerHost = 4

// Config is the complete server configuration. Build it with Default (and set
// fields in code) or with Load (env + flags), then call Validate.
type Config struct {
	Listen             string         // HTTP listen address
	PublicURL          string         // UI origin, e.g. https://speed.example.com (normalized, no trailing slash)
	TestHosts          []string       // test host origins; empty = single-origin mode
	APIToken           string         // bearer token for the CLI; empty disables bearer access
	AuthHeader         string         // header set by the auth proxy (Remote-User)
	TrustedProxies     []netip.Prefix // proxies whose X-Forwarded-For is trusted
	ServerName         string         // shown in the UI
	MaxActiveSessions  int
	MaxConnections     int
	MaxRequestDuration time.Duration
	MaxDownloadBytes   int64
	MaxUploadBytes     int64
	SessionTTL         time.Duration
	SessionMaxAge      time.Duration
	RTCEnabled         bool
	RTCUDPPort         int
	RTCPublicIPs       []string // validated IP literals
	LogLevel           slog.Level
	LogFormat          string // "json" or "text"
	MetricsListen      string // empty = off
	Dev                bool   // disables the auth check; enables debug query overrides in the UI
}

// Default returns the documented defaults (§5.2). PublicURL is empty and must
// be set (or Dev enabled) before Validate succeeds.
func Default() Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "firehose"
	}
	return Config{
		Listen:     "127.0.0.1:8080",
		AuthHeader: "Remote-User",
		TrustedProxies: []netip.Prefix{
			netip.MustParsePrefix("127.0.0.1/32"),
			netip.MustParsePrefix("::1/128"),
		},
		ServerName:         host,
		MaxActiveSessions:  1,
		MaxConnections:     32,
		MaxRequestDuration: 35 * time.Second,
		MaxDownloadBytes:   4 << 30,
		MaxUploadBytes:     512 << 20,
		SessionTTL:         120 * time.Second,
		SessionMaxAge:      5 * time.Minute,
		RTCEnabled:         true,
		RTCUDPPort:         50000,
		LogLevel:           slog.LevelInfo,
		LogFormat:          "json",
	}
}

// SingleOrigin reports whether no test hosts are configured.
func (c *Config) SingleOrigin() bool { return len(c.TestHosts) == 0 }

// EffectiveTestHosts returns the configured test hosts, or [PublicURL] in
// single-origin mode.
func (c *Config) EffectiveTestHosts() []string {
	if c.SingleOrigin() {
		return []string{c.PublicURL}
	}
	return append([]string(nil), c.TestHosts...)
}

// Logger builds the slog logger described by LogLevel and LogFormat.
func (c *Config) Logger(w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: c.LogLevel}
	if c.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}

// Load builds a Config from defaults, then environment variables (via getenv,
// typically os.Getenv; an empty value counts as unset), then flags in args.
// It applies dev defaults and validates. flag.ErrHelp is returned for -h.
func Load(args []string, getenv func(string) string) (Config, error) {
	cfg := Default()
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var envErrs []error
	for _, o := range cfg.options() {
		if v := getenv(o.env()); v != "" {
			if err := o.val.Set(v); err != nil {
				envErrs = append(envErrs, fmt.Errorf("%s: %w", o.env(), err))
			}
		}
		fs.Var(o.val, o.name, o.usage)
	}
	if len(envErrs) > 0 {
		return cfg, errors.Join(envErrs...)
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return cfg, err
		}
		return cfg, fmt.Errorf("flags: %w", err)
	}
	if fs.NArg() > 0 {
		return cfg, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	cfg.ApplyDevDefaults()
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Usage writes the flag list with env equivalents and defaults.
func Usage(w io.Writer) {
	d := Default()
	fmt.Fprintln(w, "usage: firehose serve [flags]\n\nflags (each has an env equivalent; flags override env):")
	for _, o := range d.options() {
		def := o.val.String()
		if def != "" {
			def = " (default " + def + ")"
		}
		fmt.Fprintf(w, "  --%-22s %s\n      %s%s\n", o.name, o.env(), o.usage, def)
	}
}

// ApplyDevDefaults fills in PublicURL from the listen address when Dev is on
// and PublicURL is empty, so single-origin local development works.
func (c *Config) ApplyDevDefaults() {
	if !c.Dev || c.PublicURL != "" {
		return
	}
	port := "8080"
	if _, p, err := net.SplitHostPort(c.Listen); err == nil && p != "" && p != "0" {
		port = p
	}
	c.PublicURL = "http://localhost:" + port
}

// Validate checks the configuration and normalizes origins (PublicURL and
// TestHosts lose any trailing slash). It returns all problems at once.
func (c *Config) Validate() error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if c.Listen == "" {
		bad("listen address must not be empty")
	} else if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		bad("listen address %q: %v", c.Listen, err)
	}
	if c.PublicURL == "" {
		bad("public URL is required (FIREHOSE_PUBLIC_URL / --public-url), or use --dev")
	} else if o, err := NormalizeOrigin(c.PublicURL); err != nil {
		bad("public URL: %v", err)
	} else {
		c.PublicURL = o
	}
	if len(c.TestHosts) > 0 {
		hosts := make([]string, 0, len(c.TestHosts))
		for _, h := range c.TestHosts {
			o, err := NormalizeOrigin(h)
			if err != nil {
				bad("test host %q: %v", h, err)
				continue
			}
			hosts = append(hosts, o)
		}
		c.TestHosts = hosts
	}
	if c.AuthHeader == "" {
		bad("auth header must not be empty")
	}
	if c.MaxActiveSessions <= 0 {
		bad("max active sessions must be > 0")
	}
	if c.MaxConnections <= 0 {
		bad("max connections must be > 0")
	}
	if c.MaxRequestDuration <= 0 {
		bad("max request duration must be > 0")
	}
	if c.MaxDownloadBytes <= 0 {
		bad("max download bytes must be > 0")
	}
	if c.MaxUploadBytes <= 0 {
		bad("max upload bytes must be > 0")
	}
	if c.SessionTTL <= 0 {
		bad("session TTL must be > 0")
	}
	if c.SessionMaxAge <= 0 {
		bad("session max age must be > 0")
	}
	if c.RTCUDPPort <= 0 || c.RTCUDPPort > 65535 {
		bad("RTC UDP port %d out of range", c.RTCUDPPort)
	}
	for _, ip := range c.RTCPublicIPs {
		if _, err := netip.ParseAddr(ip); err != nil {
			bad("RTC public IP %q: not an IP address", ip)
		}
	}
	if c.LogFormat != "json" && c.LogFormat != "text" {
		bad("log format %q: must be json or text", c.LogFormat)
	}
	if c.MetricsListen != "" {
		if _, _, err := net.SplitHostPort(c.MetricsListen); err != nil {
			bad("metrics listen address %q: %v", c.MetricsListen, err)
		}
	}
	return errors.Join(errs...)
}

// NormalizeOrigin checks that s is an http(s) origin (scheme://host[:port],
// optional trailing slash, nothing else) and returns it without trailing slash.
func NormalizeOrigin(s string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("missing host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("must be an origin (scheme://host[:port]) without path, query or credentials")
	}
	return strings.ToLower(u.Scheme) + "://" + strings.ToLower(u.Host), nil
}

// ParseSize parses a byte size: a plain integer, or a number with a KiB, MiB,
// GiB or TiB suffix (binary multiples; also accepts K/M/G/T and KB/MB/GB/TB as
// binary aliases). Decimal fractions are allowed with a suffix ("1.5GiB").
func ParseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty size")
	}
	i := len(s)
	for i > 0 && (s[i-1] < '0' || s[i-1] > '9') && s[i-1] != '.' {
		i--
	}
	num, unit := strings.TrimSpace(s[:i]), strings.ToLower(strings.TrimSpace(s[i:]))
	var mult int64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("size %q: unknown unit %q", s, s[i:])
	}
	if n, err := strconv.ParseInt(num, 10, 64); err == nil {
		if n < 0 {
			return 0, fmt.Errorf("size %q: negative", s)
		}
		if n > (1<<63-1)/mult {
			return 0, fmt.Errorf("size %q: overflow", s)
		}
		return n * mult, nil
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil || f < 0 || mult == 1 {
		return 0, fmt.Errorf("size %q: invalid number", s)
	}
	v := f * float64(mult)
	if v >= 1<<63 {
		return 0, fmt.Errorf("size %q: overflow", s)
	}
	return int64(v), nil
}

// FormatSize renders n with the largest exact binary unit (4GiB, 512MiB, 1000).
func FormatSize(n int64) string {
	units := []struct {
		s string
		m int64
	}{{"TiB", 1 << 40}, {"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}
	for _, u := range units {
		if n != 0 && n%u.m == 0 {
			return strconv.FormatInt(n/u.m, 10) + u.s
		}
	}
	return strconv.FormatInt(n, 10)
}
