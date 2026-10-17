package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/CicerBro/speedtest/internal/client"
)

const clientUsage = `usage: speedtest client [flags]

Runs a speed test against a speedtest server (latency, packet loss,
download, upload). Exit codes: 0 ok, 1 usage, 2 unreachable/TLS,
3 authentication, 4 a phase failed, 130 interrupted.

flags:
  -s, --server URL        server URL, the server's PUBLIC_URL (default $SPEEDTEST_SERVER)
      --token TOKEN       bearer token, the server's SPEEDTEST_API_TOKEN (default $SPEEDTEST_TOKEN; prefer the env var)
  -n, --connections N     streams per direction; 1 = single connection (default 8)
  -d, --duration D        duration per direction (default 12s)
      --grace D           warm-up excluded from the final result (default 2s)
      --only LIST         comma list of latency,down,up,loss (default all)
      --no-loss           skip the packet-loss test
  -4 / -6                 force IPv4 / IPv6
      --direct            ignore the server's test hosts; send all streams to --server
      --json              print only the result JSON (same as --format json)
      --format F          human, json or csv (default human)
      --csv-header        print the CSV header line first
  -q, --quiet             no live progress, summary only
      --interval D        repeat the test every D (e.g. 1h); 0 = run once (default 0)
      --timeout D         overall timeout for one run (default 90s)
      --insecure          skip TLS certificate verification
      --ca-file FILE      extra CA bundle (PEM)
      --version           print the version
`

// clientConfig is the parsed command line.
type clientConfig struct {
	opts      client.Options
	format    client.Format
	csvHeader bool
	quiet     bool
	interval  time.Duration
	timeout   time.Duration
	version   bool
	help      bool
}

// parseClientFlags parses args with getenv for the env defaults.
func parseClientFlags(args []string, getenv func(string) string) (clientConfig, error) {
	var (
		c                    clientConfig
		server, token        string
		only, format         string
		grace                time.Duration
		noLoss, v4, v6, json bool
	)
	fs := flag.NewFlagSet("client", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&server, "server", getenv("SPEEDTEST_SERVER"), "")
	fs.StringVar(&server, "s", getenv("SPEEDTEST_SERVER"), "")
	fs.StringVar(&token, "token", getenv("SPEEDTEST_TOKEN"), "")
	fs.IntVar(&c.opts.Connections, "connections", client.DefaultConnections, "")
	fs.IntVar(&c.opts.Connections, "n", client.DefaultConnections, "")
	fs.DurationVar(&c.opts.Duration, "duration", client.DefaultDuration, "")
	fs.DurationVar(&c.opts.Duration, "d", client.DefaultDuration, "")
	fs.DurationVar(&grace, "grace", client.DefaultGrace, "")
	fs.StringVar(&only, "only", "", "")
	fs.BoolVar(&noLoss, "no-loss", false, "")
	fs.BoolVar(&v4, "4", false, "")
	fs.BoolVar(&v6, "6", false, "")
	fs.BoolVar(&c.opts.Direct, "direct", false, "")
	fs.BoolVar(&json, "json", false, "")
	fs.StringVar(&format, "format", string(client.FormatHuman), "")
	fs.BoolVar(&c.csvHeader, "csv-header", false, "")
	fs.BoolVar(&c.quiet, "quiet", false, "")
	fs.BoolVar(&c.quiet, "q", false, "")
	fs.DurationVar(&c.interval, "interval", 0, "")
	fs.DurationVar(&c.timeout, "timeout", 90*time.Second, "")
	fs.BoolVar(&c.opts.Insecure, "insecure", false, "")
	fs.StringVar(&c.opts.CAFile, "ca-file", "", "")
	fs.BoolVar(&c.version, "version", false, "")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			c.help = true
			return c, nil
		}
		return c, &client.UsageError{Msg: err.Error()}
	}
	if c.version {
		return c, nil
	}
	if fs.NArg() > 0 {
		return c, &client.UsageError{Msg: fmt.Sprintf("unexpected arguments: %v", fs.Args())}
	}

	srv, err := client.NormalizeServer(server)
	if err != nil {
		return c, err
	}
	c.opts.Server = srv
	c.opts.Token = strings.TrimSpace(token)
	if c.opts.Connections < 1 {
		return c, &client.UsageError{Msg: "--connections must be at least 1"}
	}
	if c.opts.Duration <= 0 {
		return c, &client.UsageError{Msg: "--duration must be positive"}
	}
	switch {
	case grace < 0:
		return c, &client.UsageError{Msg: "--grace must not be negative"}
	case grace == 0:
		c.opts.NoGrace = true
	default:
		c.opts.Grace = grace
	}
	if c.interval < 0 || c.timeout < 0 {
		return c, &client.UsageError{Msg: "--interval and --timeout must not be negative"}
	}
	switch {
	case v4 && v6:
		return c, &client.UsageError{Msg: "-4 and -6 are mutually exclusive"}
	case v4:
		c.opts.IPFamily = 4
	case v6:
		c.opts.IPFamily = 6
	}

	phases, err := parseOnly(only)
	if err != nil {
		return c, err
	}
	if noLoss {
		phases.Loss = false
	}
	if phases == (client.Phases{}) {
		return c, &client.UsageError{Msg: "nothing to run (--only loss together with --no-loss)"}
	}
	c.opts.Phases = phases

	if json {
		format = string(client.FormatJSON)
	}
	if c.format, err = client.ParseFormat(format); err != nil {
		return c, err
	}
	return c, nil
}

// parseOnly parses the --only list; empty means all phases.
func parseOnly(s string) (client.Phases, error) {
	if strings.TrimSpace(s) == "" {
		return client.AllPhases(), nil
	}
	var p client.Phases
	for _, part := range strings.Split(s, ",") {
		switch strings.ToLower(strings.TrimSpace(part)) {
		case "latency":
			p.Latency = true
		case "down", "download":
			p.Download = true
		case "up", "upload":
			p.Upload = true
		case "loss":
			p.Loss = true
		case "":
		default:
			return p, &client.UsageError{Msg: fmt.Sprintf("--only: unknown phase %q (use latency,down,up,loss)", part)}
		}
	}
	return p, nil
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

func runClient(args []string) int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // a second Ctrl-C kills the process
	}()
	return clientMain(ctx, args, os.Getenv, os.Stdout, os.Stderr, isTTY(os.Stdout))
}

// clientMain is runClient with injectable I/O (tests).
func clientMain(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer, tty bool) int {
	cfg, err := parseClientFlags(args, getenv)
	if err != nil {
		fmt.Fprintf(stderr, "speedtest client: %v\n\nrun 'speedtest client -h' for help\n", err)
		return client.ExitUsage
	}
	if cfg.help {
		fmt.Fprint(stdout, clientUsage)
		return client.ExitOK
	}
	if cfg.version {
		fmt.Fprintln(stdout, version)
		return client.ExitOK
	}
	if cfg.opts.Insecure {
		fmt.Fprintln(stderr, "warning: --insecure: TLS certificate verification is disabled")
	}
	if strings.HasPrefix(cfg.opts.Server, "http://") && cfg.opts.Token != "" {
		fmt.Fprintln(stderr, "warning: sending the bearer token over plain http")
	}

	o := cfg.opts
	o.Version = version
	o.Warn = func(msg string) { fmt.Fprintln(stderr, "warning: "+msg) }
	o.Loss = rtcLoss

	live := tty && !cfg.quiet
	style := client.Style{Color: tty && getenv("NO_COLOR") == ""}
	progressOut := stdout
	if cfg.format != client.FormatHuman {
		progressOut = stderr
	}
	headerShown := false
	if live {
		o.Progress = func(p client.Progress) {
			switch p.Phase {
			case client.PhaseInit:
				if cfg.format == client.FormatHuman {
					_ = client.WriteHumanHeader(stdout, *p.Result, style)
					headerShown = true
				}
			case client.PhaseDone:
				fmt.Fprint(progressOut, "\r\x1b[K")
			default:
				if line := client.ProgressLine(p); line != "" {
					fmt.Fprint(progressOut, "\r"+line+"\x1b[K")
				}
			}
		}
	}

	if cfg.format == client.FormatCSV && cfg.csvHeader {
		_ = client.WriteCSVHeader(stdout)
	}
	for {
		headerShown = false
		runStart := time.Now()
		runCtx, cancel := ctx, context.CancelFunc(func() {})
		if cfg.timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, cfg.timeout)
		}
		res, err := client.Run(runCtx, o)
		cancel()
		code := client.ExitCode(err)
		if ctx.Err() != nil {
			code = client.ExitInterrupted
		}
		if err != nil && code != client.ExitInterrupted {
			fmt.Fprintf(stderr, "speedtest client: %v\n", err)
		}
		if res.Config.Connections > 0 && (code == client.ExitOK || code == client.ExitPhaseFailed || code == client.ExitInterrupted) {
			switch cfg.format {
			case client.FormatJSON:
				_ = client.WriteJSON(stdout, res)
			case client.FormatCSV:
				_ = client.WriteCSV(stdout, res)
			default:
				if headerShown {
					_ = client.WriteHumanBody(stdout, res, style)
				} else {
					_ = client.WriteHuman(stdout, res, style)
				}
			}
		}
		if code == client.ExitInterrupted || cfg.interval == 0 {
			return code
		}
		if code == client.ExitUsage || code == client.ExitAuth {
			return code // will not get better by retrying
		}
		wait := time.Until(runStart.Add(cfg.interval))
		if wait > 0 {
			t := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				t.Stop()
				return client.ExitInterrupted
			case <-t.C:
			}
		}
		if cfg.format == client.FormatHuman {
			fmt.Fprintln(stdout)
		}
	}
}
