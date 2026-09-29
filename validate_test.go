package rtcompare

import (
	"math"
	"slices"
	"strings"
	"testing"
	"time"
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
		t.Errorf("typical noise %v exceeds the floor %v, which is a higher quantile", v.TypicalNoise, v.NoiseFloor)
	}
	if v.NoiseFloor > v.MaxObservedNoise {
		t.Errorf("floor %v exceeds the largest difference seen %v", v.NoiseFloor, v.MaxObservedNoise)
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
		Runs: 4, InnerLoops: 1000, NoiseFloor: 0.012, TypicalNoise: 0.004, MaxObservedNoise: 0.019,
		MeanConfidence: 0.51, MedianConfidence: 0.49, FalseSignalRate: 0.25, Level: 0.95,
	}
	s := v.String()
	for _, want := range []string{"4 runs", "1000 inner loops", "noise floor", "worst seen", "mean confidence", "false signals"} {
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

func TestValidateHarnessReportsDrift(t *testing.T) {
	v, err := ValidateHarness(steadyCandidate(2), quickValidation(8))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log("\n" + v.String())

	if v.DriftRate < 0 || v.DriftRate > 1 || math.IsNaN(v.DriftRate) {
		t.Errorf("drift rate %v outside [0,1]", v.DriftRate)
	}
	if v.MedianDriftShift < 0 {
		t.Errorf("median drift shift should be an absolute value, got %v", v.MedianDriftShift)
	}
	if !strings.Contains(v.String(), "drifting runs") {
		t.Errorf("String() should report the drift rate:\n%s", v.String())
	}
}

func TestValidateHarnessDriftRateIsAFraction(t *testing.T) {
	// A run counts once even though two series are examined, so the rate can
	// never exceed one.
	v, err := ValidateHarness(steadyCandidate(1), quickValidation(5))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v.DriftRate > 1 {
		t.Errorf("drift rate %v exceeds 1; runs are probably being double counted", v.DriftRate)
	}
	if got := v.DriftRate * 5; got != math.Trunc(got) {
		t.Errorf("drift rate %v is not a multiple of 1/runs, so it is not counting whole runs", v.DriftRate)
	}
}

func TestNoiseFloorIsAQuantileNotAMaximum(t *testing.T) {
	// The floor must sit at the requested quantile of the observed differences,
	// which is what stops it from growing with Runs. Checked against the recorded
	// per-run deltas, so the relationship is verified rather than assumed.
	v, err := ValidateHarness(steadyCandidate(1), quickValidation(12))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	abs := make([]float64, len(v.Deltas))
	for i, d := range v.Deltas {
		abs[i] = math.Abs(d)
	}
	slices.Sort(abs)

	if want := quantileOfSorted(abs, NoiseFloorQuantile); v.NoiseFloor != want {
		t.Errorf("NoiseFloor = %v, want the %v quantile of the deltas, %v", v.NoiseFloor, NoiseFloorQuantile, want)
	}
	if want := abs[len(abs)-1]; v.MaxObservedNoise != want {
		t.Errorf("MaxObservedNoise = %v, want the largest observed %v", v.MaxObservedNoise, want)
	}
	if v.NoiseFloor > v.MaxObservedNoise {
		t.Errorf("a %v quantile cannot exceed the maximum: %v > %v", NoiseFloorQuantile, v.NoiseFloor, v.MaxObservedNoise)
	}

	// The sort happens on a private copy, so the recorded per-run values must
	// still be the ones the runs produced, in run order.
	if len(v.Deltas) != v.Runs {
		t.Errorf("expected %d recorded deltas, got %d", v.Runs, len(v.Deltas))
	}
}

// longestRun returns the longest stretch of identical consecutive entries.
func longestRun(log []string) int {
	longest, run := 0, 0
	for i := range log {
		if i > 0 && log[i] == log[i-1] {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	return longest
}

// TestValidatePairInterleavesTheCandidates checks that validating two
// candidates before comparing them does not leave either of them with the
// caches to itself. It belongs to the A/A validation, which Compare runs for
// both candidates before measuring them against each other; validated one after
// the other, the second one entered the comparison warm (issue #111). The
// expectation is that the batches of the two candidates alternate throughout,
// so that no candidate ever runs more than twice in a row, and that each
// candidate still gets a full validation of its own.
func TestValidatePairInterleavesTheCandidates(t *testing.T) {
	var log []string
	mk := func(name string) Candidate {
		return Candidate{Name: name, Batch: func(n uint64) {
			log = append(log, name)
			rng := NewDPRNG(0x2468)
			var acc uint64
			for range n {
				acc ^= rng.Uint64()
			}
			validateSink ^= acc
		}}
	}
	opt := quickValidation(3)
	opt.Collect.WarmupDuration = time.Millisecond
	va, vb, err := ValidatePair(mk("a"), mk("b"), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := longestRun(log); got > 2 {
		t.Errorf("a candidate ran %d times in a row during the paired validation; at most 2 keeps the caches shared", got)
	}
	for name, v := range map[string]HarnessValidation{"a": va, "b": vb} {
		if v.Runs != 3 || len(v.Deltas) != 3 || v.InnerLoops != opt.Collect.InnerLoops {
			t.Errorf("candidate %s: expected 3 runs at %d inner loops, got %d runs, %d deltas, %d inner loops",
				name, opt.Collect.InnerLoops, v.Runs, len(v.Deltas), v.InnerLoops)
		}
	}
}

// TestValidatePairCalibratesToTheLargerBatch checks that the two noise floors
// describe the batch size the comparison will actually measure at. Within the
// A/A validation, leaving InnerLoops at zero calibrates both candidates, and
// the cheaper one needs the larger batch; the pair is expected to use that
// larger size for both validations.
func TestValidatePairCalibratesToTheLargerBatch(t *testing.T) {
	opt := ValidationOptions{
		Collect:   CollectOptions{Repeats: 11, MaxQuantizationError: 0.01, WarmupDuration: time.Millisecond},
		Runs:      2,
		Resamples: 300,
	}
	va, vb, err := ValidatePair(steadyCandidate(1), steadyCandidate(8), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if va.InnerLoops != vb.InnerLoops {
		t.Errorf("the two validations used different batch sizes: %d and %d", va.InnerLoops, vb.InnerLoops)
	}
	cheap, err := CalibrateInnerLoops(steadyCandidate(1), CalibrationOptions{MaxQuantizationError: 0.01})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Calibration is a noisy search, so only the order of magnitude is
	// compared: the pair must be sized for the cheap candidate, not the costly.
	if float64(va.InnerLoops) < float64(cheap.InnerLoops)/3 {
		t.Errorf("the pair used %d inner loops, far below the %d the cheaper candidate needs", va.InnerLoops, cheap.InnerLoops)
	}
}

// TestValidatePairRejectsBadInput checks that a paired validation fails loudly
// rather than measuring something unintended. It shares its option checks with
// ValidateHarness, and is expected to name the candidate at fault when one of
// them has no Batch function.
func TestValidatePairRejectsBadInput(t *testing.T) {
	good := steadyCandidate(1)
	cases := []struct {
		name string
		a, b Candidate
		opt  ValidationOptions
		want string
	}{
		{"returns error for nil batch in A", Candidate{Name: "empty"}, good, quickValidation(3), `A ("empty")`},
		{"returns error for nil batch in B", good, Candidate{Name: "empty"}, quickValidation(3), `B ("empty")`},
		{"returns error for a single run", good, good, ValidationOptions{Runs: 1}, "at least 2"},
		{"returns error for a bad level", good, good, ValidationOptions{Runs: 3, Level: 0.4}, "Level"},
		{"returns error for bad collect options", good, good, ValidationOptions{Runs: 3, Collect: CollectOptions{Repeats: 3}}, "Repeats"},
		{"returns error when calibration fails", good, Candidate{Name: "ignores n", Batch: func(uint64) { validateSink++ }},
			ValidationOptions{Runs: 2, Collect: CollectOptions{Repeats: 11, MaxInnerLoops: 1000}}, "calibrating"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := ValidatePair(c.a, c.b, c.opt)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

// TestValidationWarmsUpForItsDurationOnlyOnce checks that a validation's cost
// does not grow with the run count through the warm-up. Within the A/A
// validation, only the first run is preceded by a warm-up of the full
// duration; the later runs follow directly on the batches before them and are
// expected to warm up by count alone.
func TestValidationWarmsUpForItsDurationOnlyOnce(t *testing.T) {
	s := schedule{warmupRounds: 3, warmupDuration: time.Second}
	if got := s.forRun(0); got.warmupDuration != time.Second || got.warmupRounds != 3 {
		t.Errorf("first run: got %+v, want the full warm-up", got)
	}
	if got := s.forRun(5); got.warmupDuration != 0 || got.warmupRounds != 3 {
		t.Errorf("later run: got %+v, want the count without the duration", got)
	}
}

// TestPairSlotsGiveBothHalvesTheSameHistory checks that a paired validation
// measures each candidate's two A/A halves under the same conditions. Within
// the A/A validation, what ran just before a batch changes what the caches
// hold, so a half that more often follows its own candidate starts warmer and
// makes identical code look different. Replaying the order the measurement
// loop uses, each candidate's two slots are expected to be preceded by that
// same candidate equally often.
func TestPairSlotsGiveBothHalvesTheSameHistory(t *testing.T) {
	var order []int // slot indices in run order
	for round := range 8 {
		for k := range pairSlots {
			order = append(order, turn(k, len(pairSlots), round%2 == 0))
		}
	}
	// Read cyclically: over an even number of rounds the sequence repeats, and
	// the first batch of a run follows the last one of the warm-up, which ran
	// in the same alternating order.
	selfPreceded := map[int]int{}
	for i := range order {
		slot, before := order[i], order[(i+len(order)-1)%len(order)]
		if pairSlots[slot] == pairSlots[before] {
			selfPreceded[slot]++
		}
	}
	halves := map[int][]int{}
	for slot, who := range pairSlots {
		halves[who] = append(halves[who], slot)
	}
	for who, slots := range halves {
		if len(slots) != 2 {
			t.Fatalf("candidate %d has %d slots, want 2", who, len(slots))
		}
		if a, b := selfPreceded[slots[0]], selfPreceded[slots[1]]; a != b {
			t.Errorf("candidate %d: slot %d follows its own candidate %d times, slot %d %d times",
				who, slots[0], a, slots[1], b)
		}
	}
}

// TestValidationBootstrapsFollowTheirSeed checks that a seeded validation is
// reproducible in everything but the measurements: the same A/A samples give
// the same confidences under the same seed, and every run of every candidate
// draws from seeds of its own.
func TestValidationBootstrapsFollowTheirSeed(t *testing.T) {
	a := []float64{10, 11, 12, 10.5, 11.5, 12.5, 10.2, 11.2, 12.2, 10.7, 11.7}
	b := []float64{10.1, 11.1, 12.1, 10.6, 11.6, 12.6, 10.3, 11.3, 12.3, 10.8, 11.8}
	opt := ValidationOptions{Resamples: 500, Level: 0.95, Seed: 5}
	first, second := newAARuns(opt, 0), newAARuns(opt, 0)
	first.add(a, b)
	second.add(a, b)
	if first.confidences[0] != second.confidences[0] || first.tieRates[0] != second.tieRates[0] {
		t.Errorf("same seed, different results: %v/%v and %v/%v", first.confidences, first.tieRates, second.confidences, second.tieRates)
	}
	seen := map[uint64]bool{}
	for stream := range uint64(2) {
		r := newAARuns(opt, stream)
		for range 3 {
			r.deltas = append(r.deltas, 0)
			ab, ba := r.seeds()
			for _, s := range []uint64{ab, ba} {
				if s == 0 || seen[s] {
					t.Fatalf("seed %d repeats or is zero", s)
				}
				seen[s] = true
			}
		}
	}
	if ab, ba := newAARuns(ValidationOptions{}, 0).seeds(); ab != 0 || ba != 0 {
		t.Errorf("an unseeded validation should draw from cryptographic randomness, got seeds %d and %d", ab, ba)
	}
}
