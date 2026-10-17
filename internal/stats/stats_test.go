package stats

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

type listCase struct {
	Name string    `json:"name"`
	In   []float64 `json:"in"`
	Want float64   `json:"want"`
}

type vectors struct {
	Median      []listCase `json:"median"`
	P90         []listCase `json:"p90"`
	Min         []listCase `json:"min"`
	Jitter      []listCase `json:"jitter"`
	Interpolate []struct {
		Name     string   `json:"name"`
		Timeline []Sample `json:"timeline"`
		T        float64  `json:"t"`
		Want     float64  `json:"want"`
	} `json:"interpolate"`
	LiveRate []struct {
		Name     string   `json:"name"`
		Timeline []Sample `json:"timeline"`
		Now      float64  `json:"now"`
		WindowMs float64  `json:"windowMs"`
		Want     float64  `json:"want"`
	} `json:"liveRate"`
	Steady []struct {
		Name     string     `json:"name"`
		Timeline []Sample   `json:"timeline"`
		GraceMs  float64    `json:"graceMs"`
		EndMs    float64    `json:"endMs"`
		Want     Throughput `json:"want"`
	} `json:"steady"`
	RollingP90 []struct {
		Name     string   `json:"name"`
		Timeline []Sample `json:"timeline"`
		GraceMs  float64  `json:"graceMs"`
		EndMs    float64  `json:"endMs"`
		Want     float64  `json:"want"`
	} `json:"rollingP90"`
	RFC3550 []struct {
		Name   string  `json:"name"`
		Probes []Probe `json:"probes"`
		Want   float64 `json:"want"`
	} `json:"rfc3550"`
	LossPct []struct {
		Name     string  `json:"name"`
		Sent     int     `json:"sent"`
		Received int     `json:"received"`
		Want     float64 `json:"want"`
	} `json:"lossPct"`
}

func load(t *testing.T) vectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "stats-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v vectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func near(got, want float64) bool {
	if got == want {
		return true
	}
	return math.Abs(got-want) <= 1e-6*math.Max(1, math.Max(math.Abs(got), math.Abs(want)))
}

func check(t *testing.T, fn, name string, got, want float64) {
	t.Helper()
	if !near(got, want) {
		t.Errorf("%s(%s) = %v, want %v", fn, name, got, want)
	}
}

func TestVectors(t *testing.T) {
	v := load(t)
	counts := map[string]int{
		"median": len(v.Median), "p90": len(v.P90), "min": len(v.Min), "jitter": len(v.Jitter),
		"interpolate": len(v.Interpolate), "liveRate": len(v.LiveRate), "steady": len(v.Steady),
		"rollingP90": len(v.RollingP90), "rfc3550": len(v.RFC3550), "lossPct": len(v.LossPct),
	}
	for k, n := range counts {
		if n == 0 {
			t.Errorf("no vectors for %s", k)
		}
	}
	for _, c := range v.Median {
		in := append([]float64(nil), c.In...)
		check(t, "Median", c.Name, Median(c.In), c.Want)
		for i := range in {
			if in[i] != c.In[i] {
				t.Errorf("Median(%s) mutated input", c.Name)
				break
			}
		}
	}
	for _, c := range v.P90 {
		in := append([]float64(nil), c.In...)
		check(t, "P90", c.Name, P90(c.In), c.Want)
		for i := range in {
			if in[i] != c.In[i] {
				t.Errorf("P90(%s) mutated input", c.Name)
				break
			}
		}
	}
	for _, c := range v.Min {
		check(t, "Min", c.Name, Min(c.In), c.Want)
	}
	for _, c := range v.Jitter {
		check(t, "Jitter", c.Name, Jitter(c.In), c.Want)
	}
	for _, c := range v.Interpolate {
		check(t, "Interpolate", c.Name, Interpolate(c.Timeline, c.T), c.Want)
	}
	for _, c := range v.LiveRate {
		check(t, "LiveRate", c.Name, LiveRate(c.Timeline, c.Now, c.WindowMs), c.Want)
	}
	for _, c := range v.Steady {
		got := Steady(c.Timeline, c.GraceMs, c.EndMs)
		check(t, "Steady.Bps", c.Name, got.Bps, c.Want.Bps)
		check(t, "Steady.Bytes", c.Name, got.Bytes, c.Want.Bytes)
		check(t, "Steady.SteadyMs", c.Name, got.SteadyMs, c.Want.SteadyMs)
		if got.Short != c.Want.Short {
			t.Errorf("Steady.Short(%s) = %v, want %v", c.Name, got.Short, c.Want.Short)
		}
	}
	for _, c := range v.RollingP90 {
		check(t, "RollingP90", c.Name, RollingP90(c.Timeline, c.GraceMs, c.EndMs), c.Want)
	}
	for _, c := range v.RFC3550 {
		check(t, "RFC3550JitterMs", c.Name, RFC3550JitterMs(c.Probes), c.Want)
	}
	for _, c := range v.LossPct {
		check(t, "LossPct", c.Name, LossPct(c.Sent, c.Received), c.Want)
	}
}

func TestLossPctClamp(t *testing.T) {
	if got := LossPct(10, -5); got != 100 {
		t.Errorf("LossPct(10,-5) = %v, want 100", got)
	}
	if got := LossPct(-1, 0); got != 0 {
		t.Errorf("LossPct(-1,0) = %v, want 0", got)
	}
}
