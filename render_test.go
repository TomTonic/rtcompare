package rtcompare

import (
	"strings"
	"testing"
)

// TestReportTextReadsInPlainSentences checks the printed output of Compare:
// a reader who has never seen the library must find the unit, which candidate
// is faster and by how much, what the noise floor means, and a verdict that
// says in words why it was reached. The output belongs to Report.String, built
// from the sentences in render.go, and is checked for the pieces a reader
// depends on rather than for the exact layout.
func TestReportTextReadsInPlainSentences(t *testing.T) {
	base := Report{
		NsPerOpA: 712.7, NsPerOpB: 1262,
		Estimate:   Estimate{Delta: 0.4352, Low: 0.4231, High: 0.4448, Level: 0.95},
		Validated:  true,
		NoiseFloor: 0.0177, Autocorrelation: 0.344, BlockLength: 5,
		Resolved: true,
		Warnings: []string{"candidate B drifted during the run"},
		Confidence: Confidences{
			{Threshold: 0.05, Confidence: 1},
		},
	}
	cases := []struct {
		name   string
		mutate func(*Report)
		want   []string
		absent []string
	}{
		{"names the unit of the per-operation cost", func(*Report) {}, []string{"A: 712.7 ns per operation", "B: 1262 ns per operation"}, nil},
		{"says how much less time the faster candidate needs", func(*Report) {},
			[]string{"A needs 43.5% less time than B: B takes 1.77 times as long as A.", "between 42.3% and 44.5% less time"}, nil},
		{"explains the noise floor", func(*Report) {}, []string{"Noise floor: 1.77%. Identical code measured against itself"}, nil},
		{"explains why blocks were used", func(*Report) {}, []string{"correlated (autocorrelation +0.34), so they were resampled in blocks of 5"}, nil},
		{"gives the verdict with its reason", func(*Report) {}, []string{"Verdict: RESOLVED. A is faster than B: the difference is real"}, nil},
		{"lists warnings under a heading", func(*Report) {}, []string{"Warnings:\n  - candidate B drifted during the run"}, nil},
		{"words a confidence line for a threshold", func(*Report) {}, []string{"Confidence that A needs at least 5% less time than B: 100.0%"}, nil},
		{"says A is slower when the estimate is negative", func(r *Report) { r.Estimate = Estimate{Delta: -0.1, Low: -0.12, High: -0.08, Level: 0.95} },
			[]string{"A needs 10% more time than B", "between 8% and 12% more time", "Verdict: RESOLVED. A is slower than B"}, nil},
		{"spells out an interval that includes zero", func(r *Report) {
			r.Estimate = Estimate{Delta: 0.01, Low: -0.02, High: 0.04, Level: 0.95}
			r.Resolved = false
		}, []string{"between 2% more time and 4% less time", "Verdict: NOT RESOLVED", "not the same as \"equally fast\""}, nil},
		{"says when no noise floor was measured", func(r *Report) { r.Validated = false }, []string{"Noise floor: not measured.", "not checked against it"}, []string{"Identical code measured"}},
		{"words a negative threshold as a tolerated slowdown", func(r *Report) { r.Confidence = Confidences{{Threshold: -0.05, Confidence: 0.9}} },
			[]string{"at most 5% more time than B: 90.0%"}, nil},
		{"words a zero threshold as no slower", func(r *Report) { r.Confidence = Confidences{{Threshold: 0, Confidence: 1}} },
			[]string{"no more time than B"}, nil},
		{"says measurements were resampled one by one when independent", func(r *Report) { r.BlockLength = 1; r.Autocorrelation = 0.02 },
			[]string{"nearly independent (autocorrelation +0.02), so they were resampled one by one"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := base
			c.mutate(&r)
			s := r.String()
			for _, w := range c.want {
				if !strings.Contains(s, w) {
					t.Errorf("output lacks %q:\n%s", w, s)
				}
			}
			for _, a := range c.absent {
				if strings.Contains(s, a) {
					t.Errorf("output contains %q:\n%s", a, s)
				}
			}
		})
	}
}

// TestDriftWarningsNameTheDirection checks the wording of the drift warning a
// reader sees when the machine did not hold still: it says which candidate,
// and whether its measurements got higher or lower, not just a signed number.
func TestDriftWarningsNameTheDirection(t *testing.T) {
	for _, c := range []struct {
		name  string
		shift float64
		want  string
	}{
		{"says lower for a negative shift", -0.0712, "7.12% lower in the second half than in the first"},
		{"says higher for a positive shift", 0.05, "5% higher in the second half than in the first"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := "its measurements were " + drift(c.shift) + " in the second half than in the first"
			if got != "its measurements were "+c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestPooledTextReadsInPlainSentences checks the printed result of a
// multi-process run: the number of processes, the difference in words, how
// much the processes disagree and a verdict.
func TestPooledTextReadsInPlainSentences(t *testing.T) {
	p := Pooled{Processes: 12, Delta: 0.041, Low: 0.03, High: 0.05, Level: 0.95,
		SpreadBetween: 0.008, SpreadWithin: 0.002, Inflation: 4, I2: 0.9, Resolved: true,
		Warnings: []string{"a warning"}}
	s := p.String()
	for _, w := range []string{"Pooled over 12 processes.", "A needs 4.1% less time than B", "The processes disagree by 0.8% (each one alone: 0.2%)",
		"about 4.0 times too narrow", "Verdict: RESOLVED. A is faster than B", "Warnings:\n  - a warning"} {
		if !strings.Contains(s, w) {
			t.Errorf("output lacks %q:\n%s", w, s)
		}
	}
	p.Resolved = false
	if s := p.String(); !strings.Contains(s, "These processes did not establish") {
		t.Errorf("unresolved output does not say so:\n%s", s)
	}
}
