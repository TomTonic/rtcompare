package rtcompare

import (
	"math"
	"strings"
	"testing"
)

var validateSink uint64

// steadyCandidate does cost units of unremovable work per operation.
func steadyCandidate(cost uint64) Candidate {
	return Candidate{
		Name: "steady",
		Batch: func(n uint64) {
			rng := NewDPRNG(0x2468)
			var acc uint64
			for range n * cost {
				acc ^= rng.Uint64()
			}
			validateSink ^= acc
		},
	}
}

// quickValidation keeps test runtime down while staying representative.
func quickValidation(runs int) ValidationOptions {
	return ValidationOptions{
		Collect:   CollectOptions{Repeats: 21, InnerLoops: 2000, GCBetween: true},
		Runs:      runs,
		Resamples: 2000,
	}
}

func TestValidateHarnessRejectsBadInput(t *testing.T) {
	good := steadyCandidate(1)
	cases := []struct {
		name string
		c    Candidate
		opt  ValidationOptions
		want string
	}{
		{"nil batch", Candidate{Name: "empty"}, quickValidation(3), "nil Batch"},
		{"negative runs", good, ValidationOptions{Runs: -1}, "must not be negative"},
		{"single run", good, ValidationOptions{Runs: 1}, "at least 2"},
		{"level too low", good, ValidationOptions{Runs: 3, Level: 0.4}, "Level"},
		{"level at one", good, ValidationOptions{Runs: 3, Level: 1.0}, "Level"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ValidateHarness(c.c, c.opt)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

func TestValidateHarnessReportsPlausibleNoise(t *testing.T) {
	v, err := ValidateHarness(steadyCandidate(1), quickValidation(8))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log("\n" + v.String())

	if v.Runs != 8 || len(v.Deltas) != 8 || len(v.Confidences) != 8 {
		t.Errorf("expected 8 runs and 8 recorded values, got %d/%d/%d", v.Runs, len(v.Deltas), len(v.Confidences))
	}
	if v.InnerLoops != 2000 {
		t.Errorf("expected the requested InnerLoops to be reported, got %d", v.InnerLoops)
	}
	if v.NoiseFloor < 0 {
		t.Errorf("noise floor must be an absolute value, got %v", v.NoiseFloor)
	}
	if v.TypicalNoise > v.NoiseFloor {
		t.Errorf("typical noise %v exceeds the floor %v, which is a maximum", v.TypicalNoise, v.NoiseFloor)
	}
	// Identical code cannot genuinely differ. Anything beyond a few percent
	// means the machine is too disturbed for the measurement to mean anything,
	// which is itself worth failing on.
	if v.NoiseFloor > 0.15 {
		t.Errorf("noise floor of %.1f%% is implausibly large for identical code; the machine may be heavily loaded", v.NoiseFloor*100)
	}
	for _, c := range v.Confidences {
		if c < 0 || c > 1 || math.IsNaN(c) {
			t.Errorf("confidence %v outside [0,1]", c)
		}
	}
	if v.Level != DefaultValidationLevel {
		t.Errorf("expected the default level %v, got %v", DefaultValidationLevel, v.Level)
	}
}

func TestValidateHarnessCalibratesOnceWhenInnerLoopsUnset(t *testing.T) {
	seen := map[uint64]int{}
	probe := Candidate{Name: "probe", Batch: func(n uint64) {
		seen[n]++
		rng := NewDPRNG(0x99)
		var acc uint64
		for range n {
			acc ^= rng.Uint64()
		}
		validateSink ^= acc
	}}

	v, err := ValidateHarness(probe, ValidationOptions{
		Collect:   CollectOptions{Repeats: 11, MaxQuantizationError: 0.02},
		Runs:      3,
		Resamples: 500,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.InnerLoops == 0 {
		t.Fatal("expected the calibrated batch size to be reported")
	}
	// Calibration probes several sizes; the measured runs must then all use the
	// single reported one, so it has to be the overwhelmingly most common n.
	if seen[v.InnerLoops] < 3*11 {
		t.Errorf("measured runs did not all use the reported batch size %d: %v", v.InnerLoops, seen)
	}
}

func TestValidateHarnessResolves(t *testing.T) {
	v := HarnessValidation{NoiseFloor: 0.01}
	for _, c := range []struct {
		diff float64
		want bool
	}{
		{0.02, true}, {-0.02, true}, {0.005, false}, {-0.005, false}, {0.01, false}, {0, false},
	} {
		if got := v.Resolves(c.diff); got != c.want {
			t.Errorf("Resolves(%v) = %v, want %v for a floor of %v", c.diff, got, c.want, v.NoiseFloor)
		}
	}
}

func TestValidateHarnessCentersOnHalf(t *testing.T) {
	// An A/A experiment compares identical code, so a setup that is not biased
	// must centre on a confidence of 0.5. This is what the API exists to check.
	//
	// The test asserts that only for the interleaved order, and only inside a
	// generous band: a single validation of a few runs has a standard error of
	// well over 0.1, far too coarse to resolve a real ordering effect. The
	// sequential figure is logged for inspection rather than asserted on. Do not
	// read a difference between the two logged numbers as evidence of one; 40
	// A/A runs per order were unable to separate them on this machine.
	c := steadyCandidate(2)
	run := func(order Order) HarnessValidation {
		opt := quickValidation(8)
		opt.Collect.Order = order
		v, err := ValidateHarness(c, opt)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return v
	}
	abba, sequential := run(OrderABBA), run(OrderSequential)
	t.Logf("ABBA:       mean confidence %.3f, noise floor %.3f%%", abba.MeanConfidence, abba.NoiseFloor*100)
	t.Logf("Sequential: mean confidence %.3f, noise floor %.3f%%", sequential.MeanConfidence, sequential.NoiseFloor*100)

	if abba.MeanConfidence < 0.05 || abba.MeanConfidence > 0.95 {
		t.Errorf("interleaved A/A mean confidence %.3f is far from the 0.5 an unbiased setup must give", abba.MeanConfidence)
	}
}

func TestHarnessValidationString(t *testing.T) {
	v := HarnessValidation{
		Runs: 4, InnerLoops: 1000, NoiseFloor: 0.012, TypicalNoise: 0.004,
		MeanConfidence: 0.51, MedianConfidence: 0.49, FalseSignalRate: 0.25, Level: 0.95,
	}
	s := v.String()
	for _, want := range []string{"4 runs", "1000 inner loops", "noise floor", "mean confidence", "false signals"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

// TestTieSplitIdentity checks the arithmetic ValidateHarness relies on to split
// ties using nothing but the public confidence function.
//
// With confAB = P(medA<medB) + P(tie) and confBA = P(medA>medB) + P(tie), and
// the three probabilities summing to one, (confAB + 1 - confBA)/2 must give
// P(medA<medB) + P(tie)/2, and confAB + confBA - 1 must give the tie rate.
func TestTieSplitIdentity(t *testing.T) {
	// Constant and identical inputs: every resampled median is the same value,
	// so every replicate ties.
	constant := make([]float64, 21)
	for i := range constant {
		constant[i] = 10
	}
	confAB := BootstrapConfidence(constant, constant, []float64{0.0}, 1000, 0)[0.0]
	confBA := BootstrapConfidence(constant, constant, []float64{0.0}, 1000, 0)[0.0]

	if confAB != 1.0 || confBA != 1.0 {
		t.Fatalf("all-tie input should give confidence 1 in both directions, got %v and %v", confAB, confBA)
	}
	if midP := (confAB + 1 - confBA) / 2; midP != 0.5 {
		t.Errorf("tie-split confidence for identical inputs should be exactly 0.5, got %v", midP)
	}
	if tie := confAB + confBA - 1; tie != 1.0 {
		t.Errorf("tie rate for identical inputs should be 1, got %v", tie)
	}

	// Cleanly separated inputs: no ties, and the tie-split value equals the
	// plain confidence.
	fast := make([]float64, 21)
	slow := make([]float64, 21)
	for i := range fast {
		fast[i] = 10
		slow[i] = 20
	}
	confAB = BootstrapConfidence(fast, slow, []float64{0.0}, 1000, 0)[0.0]
	confBA = BootstrapConfidence(slow, fast, []float64{0.0}, 1000, 0)[0.0]
	if tie := confAB + confBA - 1; tie != 0.0 {
		t.Errorf("separated inputs should produce no ties, got a rate of %v", tie)
	}
	if midP := (confAB + 1 - confBA) / 2; midP != 1.0 {
		t.Errorf("tie-split confidence should be 1 when A is always faster, got %v", midP)
	}
}

func TestValidateHarnessReportsTieRate(t *testing.T) {
	v, err := ValidateHarness(steadyCandidate(1), quickValidation(6))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.TieRate < 0 || v.TieRate > 1 || math.IsNaN(v.TieRate) {
		t.Errorf("tie rate %v outside [0,1]", v.TieRate)
	}
	if !strings.Contains(v.String(), "tied replicates") {
		t.Errorf("String() should report the tie rate:\n%s", v.String())
	}
	// Quantized timings tie often; a rate of exactly zero across every run would
	// suggest the tie accounting is not wired up.
	t.Logf("tie rate %.1f%%, mean confidence %.3f", v.TieRate*100, v.MeanConfidence)
}
