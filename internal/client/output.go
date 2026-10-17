package client

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

// Format is an output format.
type Format string

// Output formats (§10.4).
const (
	FormatHuman Format = "human"
	FormatJSON  Format = "json"
	FormatCSV   Format = "csv"
)

// ParseFormat validates a --format value.
func ParseFormat(s string) (Format, error) {
	switch f := Format(strings.ToLower(strings.TrimSpace(s))); f {
	case FormatHuman, FormatJSON, FormatCSV:
		return f, nil
	}
	return "", &UsageError{Msg: fmt.Sprintf("invalid --format %q: use human, json or csv", s)}
}

// WriteJSON writes r as one compact JSON object followed by a newline, so
// repeated runs form NDJSON.
func WriteJSON(w io.Writer, r Result) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}

// CSVHeader is the column list of WriteCSV.
var CSVHeader = []string{
	"timestamp", "server_name", "server_url", "server_version",
	"client_ip", "client_family",
	"connections", "duration_sec", "grace_sec",
	"idle_latency_ms", "idle_latency_min_ms", "idle_jitter_ms", "idle_samples",
	"download_bps", "download_p90_bps", "download_bytes", "download_steady_ms", "download_short",
	"download_latency_ms", "download_latency_p90_ms", "download_latency_samples",
	"upload_bps", "upload_p90_bps", "upload_bytes", "upload_steady_ms", "upload_short", "upload_approx",
	"upload_latency_ms", "upload_latency_p90_ms", "upload_latency_samples",
	"loss_available", "loss_down_pct", "loss_up_pct", "udp_jitter_down_ms", "udp_jitter_up_ms",
	"aborted",
}

// WriteCSVHeader writes the CSV header line.
func WriteCSVHeader(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(CSVHeader); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// WriteCSV writes r as one CSV line (columns: CSVHeader).
func WriteCSV(w io.Writer, r Result) error {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	b := strconv.FormatBool
	i := strconv.Itoa
	rec := []string{
		r.Timestamp, r.Server.Name, r.Server.URL, r.Server.Version,
		r.Client.IP, r.Client.Family,
		i(r.Config.Connections), f(r.Config.DurationSec), f(r.Config.GraceSec),
		f(r.Latency.Idle.MedianMs), f(r.Latency.Idle.MinMs), f(r.Latency.Idle.JitterMs), i(r.Latency.Idle.Samples),
		f(r.Download.Bps), f(r.Download.P90Bps), f(r.Download.Bytes), f(r.Download.SteadyMs), b(r.Download.Short),
		f(r.Latency.Download.MedianMs), f(r.Latency.Download.P90Ms), i(r.Latency.Download.Samples),
		f(r.Upload.Bps), f(r.Upload.P90Bps), f(r.Upload.Bytes), f(r.Upload.SteadyMs), b(r.Upload.Short), b(r.Upload.Approx),
		f(r.Latency.Upload.MedianMs), f(r.Latency.Upload.P90Ms), i(r.Latency.Upload.Samples),
		b(r.Loss.Available), f(r.Loss.DownPct), f(r.Loss.UpPct), f(r.Loss.JitterDownMs), f(r.Loss.JitterUpMs),
		b(r.Flags.Aborted),
	}
	cw := csv.NewWriter(w)
	if err := cw.Write(rec); err != nil {
		return err
	}
	cw.Flush()
	return cw.Error()
}

// Style controls terminal styling of the human output.
type Style struct {
	Color bool // ANSI colors (off for NO_COLOR or non-TTY)
}

func (s Style) wrap(code, text string) string {
	if !s.Color {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func (s Style) value(t string) string { return s.wrap("1;36", t) }
func (s Style) dim(t string) string   { return s.wrap("2", t) }
func (s Style) warn(t string) string  { return s.wrap("33", t) }

// FormatBps formats a bit rate with 3 significant digits in SI units
// (1 Gbps = 10⁹ bit/s).
func FormatBps(bps float64) string {
	units := []string{"bps", "kbps", "Mbps", "Gbps", "Tbps"}
	u := 0
	for u < len(units)-1 && math.Abs(bps) >= 999.5 {
		bps /= 1000
		u++
	}
	return sig3(bps) + " " + units[u]
}

// sig3 formats v with three significant digits (at least 0 decimals).
func sig3(v float64) string {
	a := math.Abs(v)
	switch {
	case a == 0:
		return "0"
	case a < 9.995:
		return strconv.FormatFloat(v, 'f', 2, 64)
	case a < 99.95:
		return strconv.FormatFloat(v, 'f', 1, 64)
	default:
		return strconv.FormatFloat(v, 'f', 0, 64)
	}
}

// FormatMs formats milliseconds: 2 decimals below 0.1 ms, else 1 decimal.
func FormatMs(ms float64) string {
	if ms < 0.095 {
		return strconv.FormatFloat(ms, 'f', 2, 64)
	}
	return strconv.FormatFloat(ms, 'f', 1, 64)
}

func formatSec(s float64) string { return strconv.FormatFloat(s, 'f', -1, 64) + " s" }

func familyLabel(f string) string {
	switch f {
	case "ipv4":
		return "IPv4"
	case "ipv6":
		return "IPv6"
	}
	return f
}

// WriteHuman writes the full human summary: header and results.
func WriteHuman(w io.Writer, r Result, st Style) error {
	if err := WriteHumanHeader(w, r, st); err != nil {
		return err
	}
	return WriteHumanBody(w, r, st)
}

// WriteHumanHeader writes the Server / Client / Mode block.
func WriteHumanHeader(w io.Writer, r Result, st Style) error {
	var b strings.Builder
	ver := r.Server.Version
	if ver != "" && ver[0] >= '0' && ver[0] <= '9' {
		ver = "v" + ver
	}
	fmt.Fprintf(&b, "Server   %s (%s) · %s\n", orUnknown(r.Server.Name), r.Server.URL, orUnknown(ver))
	if r.Client.IP != "" {
		fmt.Fprintf(&b, "Client   %s (%s)\n", r.Client.IP, familyLabel(r.Client.Family))
	} else {
		b.WriteString("Client   unknown\n")
	}
	conns := "connections"
	if r.Config.Connections == 1 {
		conns = "connection"
	}
	fmt.Fprintf(&b, "Mode     %d %s · %s per direction", r.Config.Connections, conns, formatSec(r.Config.DurationSec))
	if r.Flags.SingleOrigin {
		b.WriteString(" · single origin")
	}
	b.WriteString("\n")
	_, err := io.WriteString(w, b.String())
	return err
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// barCells is the bar width in character cells.
const barCells = 20

// bar draws v relative to full in half-cell resolution: ▕████▌    ▏.
func bar(v, full float64) string {
	halves := 0
	if full > 0 && v > 0 {
		halves = int(math.Round(v / full * barCells * 2))
	}
	halves = max(0, min(halves, barCells*2))
	s := strings.Repeat("█", halves/2)
	cells := halves / 2
	if halves%2 == 1 {
		s += "▌"
		cells++
	}
	return "▕" + s + strings.Repeat(" ", barCells-cells) + "▏"
}

// WriteHumanBody writes the result lines (a blank line first). Phases
// that did not run are left out.
func WriteHumanBody(w io.Writer, r Result, st Style) error {
	var b strings.Builder
	b.WriteString("\n")
	label := func(s string) string { return fmt.Sprintf("%-12s", s) }
	if r.Phases.Latency {
		l := r.Latency.Idle
		if l.Samples > 0 {
			fmt.Fprintf(&b, "%s%s   %s\n", label("Latency"), st.value(FormatMs(l.MedianMs)+" ms"),
				st.dim(fmt.Sprintf("(min %s, jitter %s, %d samples)", FormatMs(l.MinMs), FormatMs(l.JitterMs), l.Samples)))
		} else {
			fmt.Fprintf(&b, "%s%s\n", label("Latency"), st.warn("no samples"))
		}
	}
	full := max(r.Download.Bps, r.Upload.Bps)
	transfer := func(name string, bps float64, short, approx bool, ll LoadedLatency) {
		val := fmt.Sprintf("%-9s", FormatBps(bps))
		fmt.Fprintf(&b, "%s%s   %s", label(name), st.value(val), bar(bps, full))
		if ll.Samples > 0 {
			fmt.Fprintf(&b, " loaded latency %s ms", FormatMs(ll.MedianMs))
		}
		var notes []string
		if short {
			notes = append(notes, "short")
		}
		if approx {
			notes = append(notes, "approx")
		}
		if len(notes) > 0 {
			b.WriteString(" " + st.warn("("+strings.Join(notes, ", ")+")"))
		}
		b.WriteString("\n")
	}
	if r.Phases.Download {
		transfer("Download", r.Download.Bps, r.Download.Short, false, r.Latency.Download)
	}
	if r.Phases.Upload {
		transfer("Upload", r.Upload.Bps, r.Upload.Short, r.Upload.Approx, r.Latency.Upload)
	}
	if r.Phases.Loss {
		l := r.Loss
		if l.Available {
			fmt.Fprintf(&b, "%s%s   %s\n", label("Packet loss"),
				st.value(fmt.Sprintf("↓ %.2f %%  ↑ %.2f %%", l.DownPct, l.UpPct)),
				st.dim(fmt.Sprintf("(UDP jitter ↓ %s ms ↑ %s ms)", FormatMs(l.JitterDownMs), FormatMs(l.JitterUpMs))))
		} else {
			fmt.Fprintf(&b, "%s%s\n", label("Packet loss"), st.warn("unavailable (UDP blocked or disabled)"))
		}
	}
	if r.Flags.Aborted {
		b.WriteString("\n" + st.warn("Aborted: partial results") + "\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// ProgressLine renders a one-line live status (no newline). The caller
// prefixes "\r" and clears the rest of the line.
func ProgressLine(p Progress) string {
	el := p.Elapsed.Truncate(100 * time.Millisecond).Seconds()
	tot := p.Duration.Seconds()
	timing := fmt.Sprintf("%4.1f / %s", el, formatSec(tot))
	switch p.Phase {
	case PhaseLatency:
		lat := "–"
		if p.LatencyMs > 0 {
			lat = FormatMs(p.LatencyMs) + " ms"
		}
		return fmt.Sprintf("%-12s%-12s %s", "Latency", lat, timing)
	case PhaseDownload, PhaseUpload:
		name := "Download"
		if p.Phase == PhaseUpload {
			name = "Upload"
		}
		s := fmt.Sprintf("%-12s%-12s %s", name, FormatBps(p.Bps), timing)
		if p.LatencyMs > 0 {
			s += "   latency " + FormatMs(p.LatencyMs) + " ms"
		}
		return s
	}
	return ""
}
