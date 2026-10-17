package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/CicerBro/firehose/internal/client"
	"github.com/CicerBro/firehose/internal/config"
	"github.com/CicerBro/firehose/internal/server"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestParseClientFlagsDefaults(t *testing.T) {
	c, err := parseClientFlags(nil, env(map[string]string{"FIREHOSE_SERVER": "speed.example.com", "FIREHOSE_TOKEN": "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	o := c.opts
	if o.Server != "https://speed.example.com" || o.Token != "tok" || o.Connections != 8 ||
		o.Duration != 12*time.Second || o.Grace != 2*time.Second || o.Phases != client.AllPhases() ||
		o.IPFamily != 0 || o.Direct || o.Insecure {
		t.Errorf("defaults: %+v", o)
	}
	if c.format != client.FormatHuman || c.quiet || c.interval != 0 || c.timeout != 90*time.Second {
		t.Errorf("defaults: %+v", c)
	}
}

func TestParseClientFlags(t *testing.T) {
	c, err := parseClientFlags([]string{
		"-s", "http://127.0.0.1:8080", "--token", "flagtok", "-n", "1", "-d", "6s", "--grace", "0",
		"--only", "latency,down", "-6", "--direct", "--json", "-q", "--interval", "1h", "--timeout", "30s", "--insecure",
	}, env(map[string]string{"FIREHOSE_TOKEN": "envtok"}))
	if err != nil {
		t.Fatal(err)
	}
	o := c.opts
	if o.Server != "http://127.0.0.1:8080" || o.Token != "flagtok" || o.Connections != 1 || o.Duration != 6*time.Second ||
		!o.NoGrace || o.Phases != (client.Phases{Latency: true, Download: true}) || o.IPFamily != 6 || !o.Direct || !o.Insecure {
		t.Errorf("opts: %+v", o)
	}
	if c.format != client.FormatJSON || !c.quiet || c.interval != time.Hour || c.timeout != 30*time.Second {
		t.Errorf("cfg: %+v", c)
	}

	c, err = parseClientFlags([]string{"--server", "https://x.example", "--no-loss", "--format", "csv", "--csv-header", "-4"}, env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.opts.Phases.Loss || !c.opts.Phases.Download || c.format != client.FormatCSV || !c.csvHeader || c.opts.IPFamily != 4 {
		t.Errorf("cfg: %+v", c)
	}
}

func TestParseClientFlagsErrors(t *testing.T) {
	srv := []string{"-s", "https://x.example"}
	for _, args := range [][]string{
		{},                      // no server
		{"-s", "ftp://x"},       // bad scheme
		append(srv, "-4", "-6"), // exclusive
		append(srv, "-n", "0"),
		append(srv, "-d", "0s"),
		append(srv, "--grace", "-1s"),
		append(srv, "--only", "latency,bogus"),
		append(srv, "--only", "loss", "--no-loss"),
		append(srv, "--format", "xml"),
		append(srv, "--bogus"),
		append(srv, "extra"),
	} {
		_, err := parseClientFlags(args, env(nil))
		if client.ExitCode(err) != client.ExitUsage {
			t.Errorf("%v: err %v, want usage error", args, err)
		}
	}
}

func TestClientMainUsageVersion(t *testing.T) {
	var out, errb bytes.Buffer
	if code := clientMain(context.Background(), []string{"--version"}, env(nil), &out, &errb, false); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Errorf("--version: %d %q", code, out.String())
	}
	out.Reset()
	if code := clientMain(context.Background(), []string{"-h"}, env(nil), &out, &errb, false); code != 0 || !strings.Contains(out.String(), "--connections") {
		t.Errorf("-h: %d %q", code, out.String())
	}
	if code := clientMain(context.Background(), nil, env(nil), &out, &errb, false); code != 1 {
		t.Errorf("no server: exit %d, want 1", code)
	}
}

const cmdTestToken = "cmd-test-token"

func startTestServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Listen = ln.Addr().String()
	cfg.PublicURL = "http://" + ln.Addr().String()
	cfg.APIToken = cmdTestToken
	cfg.RTCEnabled = false
	cfg.MaxUploadBytes = 16 << 20
	srv, err := server.New(cfg, server.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); <-done })
	return cfg.PublicURL
}

func TestClientMainEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	url := startTestServer(t)
	base := []string{"-s", url, "-n", "2", "-d", "1500ms", "--grace", "500ms", "--only", "down,up"}

	t.Run("auth", func(t *testing.T) {
		var out, errb bytes.Buffer
		if code := clientMain(context.Background(), base, env(nil), &out, &errb, false); code != 3 {
			t.Errorf("no token: exit %d, want 3 (%s)", code, errb.String())
		}
		if code := clientMain(context.Background(), base, env(map[string]string{"FIREHOSE_TOKEN": "wrong"}), &out, &errb, false); code != 3 {
			t.Errorf("wrong token: exit %d, want 3", code)
		}
		if out.Len() != 0 {
			t.Errorf("stdout not empty on auth failure: %q", out.String())
		}
	})

	t.Run("json", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := clientMain(context.Background(), append(base, "--json"), env(map[string]string{"FIREHOSE_TOKEN": cmdTestToken}), &out, &errb, true)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		var r client.Result
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("stdout is not one JSON object: %v\n%q", err, out.String())
		}
		if r.Download.Bps <= 0 || r.Upload.Bps <= 0 || r.Config.Connections != 2 {
			t.Errorf("result: %+v", r)
		}
		if strings.Contains(out.String(), "\r") {
			t.Error("progress leaked into JSON stdout")
		}
	})

	t.Run("csv", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := clientMain(context.Background(), append(base, "--format", "csv", "--csv-header", "--token", cmdTestToken), env(nil), &out, &errb, false)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) != 2 || !strings.HasPrefix(lines[0], "timestamp,") {
			t.Errorf("csv output: %q", out.String())
		}
	})

	t.Run("human non-tty", func(t *testing.T) {
		var out, errb bytes.Buffer
		code := clientMain(context.Background(), append(base, "--token", cmdTestToken), env(nil), &out, &errb, false)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, errb.String())
		}
		s := out.String()
		if strings.Contains(s, "\r") || strings.Contains(s, "\x1b[") {
			t.Errorf("non-TTY output has progress or color: %q", s)
		}
		if !strings.Contains(s, "Download") || !strings.Contains(s, "Upload") || strings.Contains(s, "Latency") {
			t.Errorf("human output: %q", s)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		var out, errb bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(1200*time.Millisecond, cancel)
		args := []string{"-s", url, "-n", "2", "-d", "10s", "--only", "down,up", "--json", "--token", cmdTestToken}
		code := clientMain(ctx, args, env(nil), &out, &errb, false)
		if code != 130 {
			t.Fatalf("exit %d, want 130 (%s)", code, errb.String())
		}
		var r client.Result
		if err := json.Unmarshal(out.Bytes(), &r); err != nil || !r.Flags.Aborted {
			t.Errorf("partial result: %v aborted=%v %q", err, r.Flags.Aborted, out.String())
		}
	})

	t.Run("interval", func(t *testing.T) {
		var out, errb bytes.Buffer
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		args := []string{"-s", url, "-n", "1", "-d", "300ms", "--grace", "0", "--only", "down", "--json", "--interval", "700ms", "--token", cmdTestToken}
		time.AfterFunc(1800*time.Millisecond, cancel) // runs at 0, 0.7, 1.4 s
		code := clientMain(ctx, args, env(nil), &out, &errb, false)
		if code != 130 {
			t.Errorf("exit %d, want 130", code)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		if len(lines) < 2 {
			t.Fatalf("want ≥2 NDJSON lines, got %q", out.String())
		}
		for _, l := range lines {
			var r client.Result
			if err := json.Unmarshal([]byte(l), &r); err != nil {
				t.Errorf("line %q: %v", l, err)
			}
		}
	})
}
