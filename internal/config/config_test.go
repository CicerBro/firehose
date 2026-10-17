package config

import (
	"errors"
	"flag"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		err  bool
	}{
		{"1000", 1000, false},
		{"0", 0, false},
		{"4GiB", 4 << 30, false},
		{"512MiB", 512 << 20, false},
		{"256 KiB", 256 << 10, false},
		{"1.5GiB", 3 << 29, false},
		{"2g", 2 << 30, false},
		{"10MB", 10 << 20, false},
		{"1TiB", 1 << 40, false},
		{"", 0, true},
		{"abc", 0, true},
		{"-1", 0, true},
		{"5XiB", 0, true},
		{"1.5", 0, true},
		{"99999999999TiB", 0, true},
	}
	for _, c := range cases {
		got, err := ParseSize(c.in)
		if (err != nil) != c.err {
			t.Errorf("ParseSize(%q) err=%v, wantErr=%v", c.in, err, c.err)
			continue
		}
		if !c.err && got != c.want {
			t.Errorf("ParseSize(%q)=%d, want %d", c.in, got, c.want)
		}
	}
}

func TestFormatSize(t *testing.T) {
	for in, want := range map[int64]string{4 << 30: "4GiB", 512 << 20: "512MiB", 1000: "1000", 1024: "1KiB", 0: "0"} {
		if got := FormatSize(in); got != want {
			t.Errorf("FormatSize(%d)=%q want %q", in, got, want)
		}
	}
}

func TestLoadDefaultsRequirePublicURL(t *testing.T) {
	_, err := Load(nil, envMap(nil))
	if err == nil || !strings.Contains(err.Error(), "public URL is required") {
		t.Fatalf("want public URL error, got %v", err)
	}
}

func TestLoadEnvAndFlags(t *testing.T) {
	env := map[string]string{
		"FIREHOSE_PUBLIC_URL":           "https://Speed.Example.com/",
		"FIREHOSE_TEST_HOSTS":           "https://s1.speed.example.com, https://s2.speed.example.com/",
		"FIREHOSE_MAX_CONNECTIONS":      "16",
		"FIREHOSE_MAX_DOWNLOAD_BYTES":   "1GiB",
		"FIREHOSE_MAX_REQUEST_DURATION": "20s",
		"FIREHOSE_TRUSTED_PROXIES":      "10.0.0.0/8,::1/128,192.168.1.1",
		"FIREHOSE_RTC_ENABLED":          "false",
		"FIREHOSE_RTC_PUBLIC_IPS":       "203.0.113.5,2001:db8::1",
		"FIREHOSE_LOG_LEVEL":            "debug",
		"FIREHOSE_API_TOKEN":            "secret",
	}
	cfg, err := Load([]string{"--max-connections", "24", "--max-upload-bytes=64MiB", "--session-ttl", "30s"}, envMap(env))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL != "https://speed.example.com" {
		t.Errorf("PublicURL=%q", cfg.PublicURL)
	}
	if len(cfg.TestHosts) != 2 || cfg.TestHosts[1] != "https://s2.speed.example.com" {
		t.Errorf("TestHosts=%v", cfg.TestHosts)
	}
	if cfg.MaxConnections != 24 {
		t.Errorf("flag should override env: MaxConnections=%d", cfg.MaxConnections)
	}
	if cfg.MaxDownloadBytes != 1<<30 || cfg.MaxUploadBytes != 64<<20 {
		t.Errorf("sizes: %d %d", cfg.MaxDownloadBytes, cfg.MaxUploadBytes)
	}
	if cfg.MaxRequestDuration != 20*time.Second || cfg.SessionTTL != 30*time.Second {
		t.Errorf("durations: %v %v", cfg.MaxRequestDuration, cfg.SessionTTL)
	}
	if len(cfg.TrustedProxies) != 3 || cfg.TrustedProxies[2].String() != "192.168.1.1/32" {
		t.Errorf("TrustedProxies=%v", cfg.TrustedProxies)
	}
	if cfg.RTCEnabled || len(cfg.RTCPublicIPs) != 2 {
		t.Errorf("rtc: %v %v", cfg.RTCEnabled, cfg.RTCPublicIPs)
	}
	if cfg.LogLevel != slog.LevelDebug || cfg.APIToken != "secret" {
		t.Errorf("level=%v token=%q", cfg.LogLevel, cfg.APIToken)
	}
	if cfg.SingleOrigin() {
		t.Error("expected multi-origin")
	}
}

func TestDevDefaultsPublicURL(t *testing.T) {
	cfg, err := Load([]string{"--dev", "--listen", "127.0.0.1:9999"}, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Dev || cfg.PublicURL != "http://localhost:9999" {
		t.Fatalf("dev=%v public=%q", cfg.Dev, cfg.PublicURL)
	}
	if !cfg.SingleOrigin() || cfg.EffectiveTestHosts()[0] != cfg.PublicURL {
		t.Fatalf("single origin hosts=%v", cfg.EffectiveTestHosts())
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string][]string{
		"invalid CIDR":      {"--public-url=https://a.example", "--trusted-proxies=10.0.0.0/33"},
		"scheme":            {"--public-url=ftp://a.example"},
		"must be an origin": {"--public-url=https://a.example/path"},
		"test host":         {"--public-url=https://a.example", "--test-hosts=notaurl"},
		"max connections":   {"--public-url=https://a.example", "--max-connections=0"},
		"invalid duration":  {"--public-url=https://a.example", "--session-ttl=5"},
		"unknown unit":      {"--public-url=https://a.example", "--max-download-bytes=5PB"},
		"log format":        {"--public-url=https://a.example", "--log-format=xml"},
		"RTC public IP":     {"--public-url=https://a.example", "--rtc-public-ips=nope"},
		"UDP port":          {"--public-url=https://a.example", "--rtc-udp-port=70000"},
		"unexpected":        {"--public-url=https://a.example", "extra"},
	}
	for want, args := range cases {
		_, err := Load(args, envMap(nil))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("args %v: want error containing %q, got %v", args, want, err)
		}
	}
	_, err := Load(nil, envMap(map[string]string{"FIREHOSE_MAX_CONNECTIONS": "x", "FIREHOSE_PUBLIC_URL": "https://a.example"}))
	if err == nil || !strings.Contains(err.Error(), "FIREHOSE_MAX_CONNECTIONS") {
		t.Errorf("env error should name the variable: %v", err)
	}
}

func TestHelp(t *testing.T) {
	if _, err := Load([]string{"-h"}, envMap(nil)); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("want ErrHelp, got %v", err)
	}
	var b strings.Builder
	Usage(&b)
	if !strings.Contains(b.String(), "FIREHOSE_MAX_CONNECTIONS") || !strings.Contains(b.String(), "4GiB") {
		t.Fatalf("usage missing content:\n%s", b.String())
	}
}

func TestDefaultConstructibleInCode(t *testing.T) {
	cfg := Default()
	cfg.PublicURL = "http://127.0.0.1:1234"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
}
