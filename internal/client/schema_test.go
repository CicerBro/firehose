package client

import (
	"fmt"
	"sort"
	"strings"
)

// resultSchema lists every §7.5 field with its JSON type.
var resultSchema = map[string]string{
	"version": "number", "timestamp": "string",
	"server": "object", "server.name": "string", "server.url": "string", "server.version": "string",
	"client": "object", "client.ip": "string", "client.family": "string", "client.userAgent": "string",
	"config": "object", "config.connections": "number", "config.durationSec": "number", "config.graceSec": "number", "config.loss": "bool",
	"latency": "object", "latency.idle": "object", "latency.download": "object", "latency.upload": "object",
	"latency.idle.medianMs": "number", "latency.idle.minMs": "number", "latency.idle.jitterMs": "number", "latency.idle.samples": "number",
	"latency.download.medianMs": "number", "latency.download.p90Ms": "number", "latency.download.samples": "number",
	"latency.upload.medianMs": "number", "latency.upload.p90Ms": "number", "latency.upload.samples": "number",
	"download": "object", "download.bps": "number", "download.p90bps": "number", "download.bytes": "number", "download.steadyMs": "number", "download.short": "bool",
	"upload": "object", "upload.bps": "number", "upload.p90bps": "number", "upload.bytes": "number", "upload.steadyMs": "number", "upload.short": "bool", "upload.approx": "bool",
	"loss": "object", "loss.available": "bool", "loss.downPct": "number", "loss.upPct": "number", "loss.jitterDownMs": "number", "loss.jitterUpMs": "number",
	"loss.sentDown": "number", "loss.sentUp": "number", "loss.reorderedDown": "number",
	"flags": "object", "flags.tabHidden": "bool", "flags.singleOrigin": "bool", "flags.aborted": "bool",
}

// validateSchema checks that m (a decoded result) has exactly the §7.5
// fields with the right JSON types.
func validateSchema(m map[string]any) error {
	seen := map[string]bool{}
	var errs []string
	var walk func(prefix string, v map[string]any)
	walk = func(prefix string, v map[string]any) {
		for k, x := range v {
			path := k
			if prefix != "" {
				path = prefix + "." + k
			}
			seen[path] = true
			want, ok := resultSchema[path]
			if !ok {
				errs = append(errs, "unexpected field "+path)
				continue
			}
			var got string
			switch x := x.(type) {
			case float64:
				got = "number"
			case string:
				got = "string"
			case bool:
				got = "bool"
			case map[string]any:
				got = "object"
				walk(path, x)
			default:
				got = fmt.Sprintf("%T", x)
			}
			if got != want {
				errs = append(errs, fmt.Sprintf("%s: %s, want %s", path, got, want))
			}
		}
	}
	walk("", m)
	for k := range resultSchema {
		if !seen[k] {
			errs = append(errs, "missing field "+k)
		}
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("schema: %s", strings.Join(errs, "; "))
	}
	return nil
}
