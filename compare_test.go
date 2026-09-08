package rtcompare

import (
	"math"
	"strings"
	"testing"
)

var compareSink uint64

// scaledCandidate does mult units of unremovable work per operation. Two of
// them differing only in mult have a known true relative difference, because
// the loop overhead scales with the work rather than sitting beside it.
func scaledCandidate(name string, mult uint64) Candidate {
	return Candidate{Name: name, Batch: func(n uint64) {
		var acc uint64
		for i := uint64(0); i < n*mult; i++ {
			acc = acc*31 + i
		}
		compareSink ^= acc
	}}
}

// fastCompare keeps the tests quick: a fixed batch size, so that no time goes
// into calibration, and few validation runs and resamples. Real use wants the
// defaults.
//
// The batch is nevertheless long enough to be worth measuring, and the repeat
// count high enough for the median to survive a disturbed batch or two. An
// earlier version used 3000 inner loops and 21 repeats, which was comfortable
// on a quiet laptop and far too coarse on a shared CI runner: it tied in a
// third of replicates there and put the point estimate 12% off the truth.
func fastCompare() CompareOptions {
	return CompareOptions{
		Collect:        CollectOptions{Repeats: 51, InnerLoops: 20000},
		ValidationRuns: 3,
		Resamples:      600,
	}
}

func TestCompareRejectsBadInput(t *testing.T) {
	good := scaledCandidate("good", 1)
	cases := []struct {
		name string
		a, b Candidate
		opt  CompareOptions
		want string
	}{
		{"nil A", Candidate{Name: "x"}, good, fastCompare(), "nil Batch"},
		{"nil B", good, Candidate{Name: "y"}, fastCompare(), "nil Batch"},
		{"NaN threshold", good, good, CompareOptions{
			Collect: CollectOptions{Repeats: 51, InnerLoops: 20000}, SkipValidation: true,
			Thresholds: []float64{0.1, math.NaN()},
		}, "NaN"},
		{"too few repeats", good, good, CompareOptions{
			Collect: CollectOptions{Repeats: 3, InnerLoops: 20000}, SkipValidation: true,
		}, "at least"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compare(c.a, c.b, c.opt)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

func TestCompareResolvesARealDifference(t *testing.T) {
	// B does exactly twice A's work, loop included, so the true relative
	// difference is 0.5 and no attenuation can shrink it.
	r, err := Compare(scaledCandidate("x1", 1), scaledCandidate("x2", 2), fastCompare())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Log("\n" + r.String())

	if !r.Resolved {
		t.Errorf("a 50%% difference should resolve; warnings: %v", r.Warnings)
	}
	// A loose band on the magnitude, because this is one measurement on whatever
	// machine happens to be running it, and a shared CI runner has moved the
	// point estimate 12% from the truth while reporting an interval that
	// contained it. The tight check on the magnitude lives in
	// TestCollectResolvesBelowTheClock, which calibrates the batch properly and
	// judges three runs by the middle one; what matters here is that Compare
	// wires the pieces together and does not, say, invert the comparison.
	if math.Abs(r.Estimate.Delta-0.5) > 0.15 {
		t.Errorf("estimated difference %.4f is not within 0.15 of the true 0.50", r.Estimate.Delta)
	}
	// The harness's own statement of its uncertainty has to be honest about the
	// truth even when the point estimate wanders.
	if r.Estimate.Low > 0.5 || r.Estimate.High < 0.5 {
		t.Logf("note: the %.0f%% interval [%.2f%%, %.2f%%] misses the true 50%%, which happens at the nominal rate",
			r.Estimate.Level*100, r.Estimate.Low*100, r.Estimate.High*100)
	}
	if !r.Estimate.Excludes(0) {
		t.Errorf("interval [%v, %v] should exclude zero", r.Estimate.Low, r.Estimate.High)
	}
	if !r.Validated {
		t.Error("validation should have run by default")
	}
	if r.NoiseFloor <= 0 || r.NoiseFloor > 0.2 {
		t.Errorf("noise floor %v is not a plausible fraction for identical code", r.NoiseFloor)
	}
	if r.NsPerOpA >= r.NsPerOpB {
		t.Errorf("A does half the work, so it must be cheaper: %v vs %v", r.NsPerOpA, r.NsPerOpB)
	}
	if len(r.SamplesA) != 51 || len(r.SamplesB) != 51 {
		t.Errorf("expected 51 samples each, got %d and %d", len(r.SamplesA), len(r.SamplesB))
	}
	if r.DriftA.N != 51 || r.DriftB.N != 51 {
		t.Errorf("both series should have been tested for drift, got N=%d and N=%d", r.DriftA.N, r.DriftB.N)
	}
}

func TestCompareDoesNotResolveIdenticalCode(t *testing.T) {
	// The case that matters most: how often does this cry wolf? Identical
	// candidates cannot differ, so every Resolved is a false positive.
	//
	// This is a rate rather than a single verdict, and asserting it on one run
	// would be both flaky and weaker than the truth. Measured at these settings
	// over 200 runs, and again over 200 under the atomic coverage
	// instrumentation the CI uses, none resolved either time. The two conditions
	// catch different things: the interval excluded zero in none of those runs,
	// while the difference cleared the noise floor in 15% to 20% of them, which
	// is roughly what a 90th-percentile floor implies. The allowance below is
	// for a badly disturbed machine, and is still tight enough to catch either
	// condition being dropped, since dropping the interval would take the rate
	// to one run in five.
	const (
		trials  = 30
		allowed = 2
	)
	same := scaledCandidate("same", 1)
	resolved := 0
	var lastResolved, lastUnresolved Report
	for range trials {
		r, err := Compare(same, same, fastCompare())
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if r.Resolved {
			resolved++
			lastResolved = r
		} else {
			lastUnresolved = r
			if len(r.Warnings) == 0 {
				t.Errorf("an unresolved comparison of identical code should explain itself:\n%s", r)
			}
		}
	}
	t.Logf("%d of %d runs of identical code resolved\n%s", resolved, trials, lastUnresolved.String())
	if resolved > allowed {
		t.Errorf("identical code resolved in %d of %d runs, more than the %d allowed; last one:\n%s",
			resolved, trials, allowed, lastResolved.String())
	}
}

func TestCompareSkipValidationWarnsAndIsCheaper(t *testing.T) {
	opt := fastCompare()
	opt.SkipValidation = true
	r, err := Compare(scaledCandidate("x1", 1), scaledCandidate("x2", 2), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Validated {
		t.Error("validation should have been skipped")
	}
	if r.NoiseFloor != 0 {
		t.Errorf("an unmeasured noise floor must be zero, got %v", r.NoiseFloor)
	}
	found := false
	for _, w := range r.Warnings {
		if strings.Contains(w, "noise floor is unknown") {
			found = true
		}
	}
	if !found {
		t.Errorf("skipping validation must be warned about, got %v", r.Warnings)
	}
	// The autocorrelation still has to come from somewhere.
	if math.IsNaN(r.Autocorrelation) {
		t.Error("autocorrelation should fall back to the measured run")
	}
}

func TestCompareReportsThresholdConfidences(t *testing.T) {
	opt := fastCompare()
	opt.Thresholds = []float64{0.0, 0.2, 0.45, 0.9}
	r, err := Compare(scaledCandidate("x1", 1), scaledCandidate("x2", 2), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(r.Confidence) != 4 {
		t.Fatalf("expected a confidence per threshold, got %v", r.Confidence)
	}
	// A is 50% faster, so easy thresholds are near certain and 90% is hopeless.
	if r.Confidence[0.2] < 0.95 {
		t.Errorf("confidence at 20%% should be near 1 for a 50%% difference, got %v", r.Confidence[0.2])
	}
	if r.Confidence[0.9] > 0.05 {
		t.Errorf("confidence at 90%% should be near 0 for a 50%% difference, got %v", r.Confidence[0.9])
	}
	// Confidence must not increase with a harder threshold.
	previous := 1.1
	for _, tr := range []float64{0.0, 0.2, 0.45, 0.9} {
		if r.Confidence[tr] > previous {
			t.Errorf("confidence rose from %v to %v at a harder threshold %v", previous, r.Confidence[tr], tr)
		}
		previous = r.Confidence[tr]
	}
	if r.Confidence == nil {
		t.Error("thresholds were requested, so Confidence must not be nil")
	}
}

func TestCompareNoThresholdsLeavesConfidenceNil(t *testing.T) {
	opt := fastCompare()
	opt.SkipValidation = true
	r, err := Compare(scaledCandidate("x1", 1), scaledCandidate("x2", 2), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Confidence != nil {
		t.Errorf("no thresholds were requested, so Confidence should be nil, got %v", r.Confidence)
	}
}

func TestCompareChoosesBlocksFromAutocorrelation(t *testing.T) {
	// The decision has to follow the measurement rather than a fixed choice.
	// Driving it directly is the only way to check both branches reliably, so
	// this exercises the rule on a synthesised report.
	for _, c := range []struct {
		auto float64
		want bool
	}{
		{0.0, false}, {AutocorrelationThreshold, false}, {AutocorrelationThreshold + 0.01, true}, {0.6, true},
	} {
		blocks := c.auto > AutocorrelationThreshold
		if blocks != c.want {
			t.Errorf("autocorrelation %v: blocks %v, want %v", c.auto, blocks, c.want)
		}
	}

	// And end to end: with independent samples the ordinary bootstrap must be
	// chosen, which is a block length of one.
	opt := fastCompare()
	opt.SkipValidation = true
	r, err := Compare(scaledCandidate("x1", 1), scaledCandidate("x2", 2), opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if r.Autocorrelation <= AutocorrelationThreshold && r.BlockLength != 1 {
		t.Errorf("autocorrelation %v is below the threshold, so the block length should be 1, got %d",
			r.Autocorrelation, r.BlockLength)
	}
	if r.Autocorrelation > AutocorrelationThreshold && r.BlockLength <= 1 {
		t.Errorf("autocorrelation %v is above the threshold, so blocks should have been used, got %d",
			r.Autocorrelation, r.BlockLength)
	}
}

func TestCompareHoldsBatchSizeFixedAcrossValidationAndMeasurement(t *testing.T) {
	// The noise floor has to describe the setup the measurement used. If
	// validation and measurement calibrated separately they could differ, so
	// every batch in the whole call must see the same n.
	seen := map[uint64]int{}
	probe := func(name string) Candidate {
		return Candidate{Name: name, Batch: func(n uint64) {
			seen[n]++
			var acc uint64
			for i := uint64(0); i < n; i++ {
				acc = acc*31 + i
			}
			compareSink ^= acc
		}}
	}
	r, err := Compare(probe("a"), probe("b"), CompareOptions{
		Collect:        CollectOptions{Repeats: 11, MaxQuantizationError: 0.02},
		ValidationRuns: 2,
		Resamples:      300,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.Validated {
		t.Fatal("expected validation to have run")
	}
	if r.ValidationA.InnerLoops != r.ValidationB.InnerLoops {
		t.Errorf("the two validations used different batch sizes: %d and %d",
			r.ValidationA.InnerLoops, r.ValidationB.InnerLoops)
	}
	// Calibration probes a range of sizes, so the settled size has to be the
	// overwhelmingly most common one rather than the only one.
	settled := r.ValidationA.InnerLoops
	measured := 2*2*11 + 11 // two validations of 2 runs, plus the comparison
	if seen[settled] < measured {
		t.Errorf("not every measured batch used the settled size %d: %v", settled, seen)
	}
}

func TestReportString(t *testing.T) {
	r := Report{
		NsPerOpA: 1.5, NsPerOpB: 3.0,
		Estimate:   Estimate{Delta: 0.5, Low: 0.45, High: 0.55, Level: 0.95},
		Validated:  true,
		NoiseFloor: 0.01, Autocorrelation: 0.35, BlockLength: 5,
		Resolved:   true,
		Warnings:   []string{"something was off"},
		Confidence: map[float64]float64{0.2: 0.99, 0.0: 1.0},
	}
	s := r.String()
	for _, want := range []string{"per op", "difference", "noise floor", "blocks of 5", "resolved: A is faster", "warning: something was off", "confidence"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
	// Thresholds must print in a stable ascending order.
	if i, j := strings.Index(s, "0.00%"), strings.Index(s, "20.00%"); i < 0 || j < 0 || i > j {
		t.Errorf("confidence lines are not in ascending threshold order:\n%s", s)
	}

	unresolved := Report{Estimate: Estimate{Delta: 0.001}, Autocorrelation: 0.0, BlockLength: 1}
	if s := unresolved.String(); !strings.Contains(s, "not resolved") || !strings.Contains(s, "not measured") {
		t.Errorf("an unvalidated, unresolved report should say so:\n%s", s)
	}
	slower := Report{Estimate: Estimate{Delta: -0.5}, Resolved: true, Validated: true, BlockLength: 1}
	if s := slower.String(); !strings.Contains(s, "A is slower") || strings.Contains(s, "A is faster") {
		t.Errorf("a negative difference should read as A being slower:\n%s", s)
	}
}

func TestSortedKeys(t *testing.T) {
	if got := sortedKeys(nil); got != nil {
		t.Errorf("an empty map has no keys, got %v", got)
	}
	got := sortedKeys(map[float64]float64{0.3: 1, -0.1: 1, 0.0: 1})
	want := []float64{-0.1, 0.0, 0.3}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestComparePropagatesFailures(t *testing.T) {
	// A batch that ignores n cannot be sized, and the error has to name the
	// candidate rather than surfacing as a bare calibration failure.
	ignoresN := Candidate{Name: "ignores n", Batch: func(uint64) { compareSink++ }}
	good := scaledCandidate("good", 1)

	for _, c := range []struct {
		name string
		a, b Candidate
		opt  CompareOptions
		want []string
	}{
		{"calibration of A", ignoresN, good, CompareOptions{SkipValidation: true}, []string{"calibrating", "A"}},
		{"calibration of B", good, ignoresN, CompareOptions{SkipValidation: true}, []string{"calibrating", "B"}},
		{"validation runs", good, good, CompareOptions{
			Collect: CollectOptions{Repeats: 11, InnerLoops: 20000}, ValidationRuns: 1, Resamples: 300,
		}, []string{"validating", "at least 2"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Compare(c.a, c.b, c.opt)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestReportWarnings(t *testing.T) {
	// The warning rules, driven directly. Some of them describe conditions a
	// healthy machine will not produce on demand, and every one of them is a
	// sentence a reader has to be able to act on.
	cases := []struct {
		name   string
		report Report
		want   string
		absent string
	}{
		{
			name:   "validation skipped",
			report: Report{Estimate: Estimate{Delta: 0.5, Low: 0.4, High: 0.6}},
			want:   "noise floor is unknown",
		},
		{
			name: "inside the noise floor",
			report: Report{
				Validated: true, NoiseFloor: 0.05,
				Estimate: Estimate{Delta: 0.01, Low: 0.005, High: 0.02},
			},
			want: "does not clear",
		},
		{
			name: "clears the floor",
			report: Report{
				Validated: true, NoiseFloor: 0.01,
				Estimate: Estimate{Delta: 0.5, Low: 0.4, High: 0.6},
			},
			absent: "does not clear",
		},
		{
			name: "interval spans zero",
			report: Report{
				Validated: true, NoiseFloor: 0.001,
				Estimate: Estimate{Delta: 0.02, Low: -0.03, High: 0.07},
			},
			want: "includes zero",
		},
		{
			name: "drift in one series",
			report: Report{
				Validated: true, NoiseFloor: 0.01,
				Estimate: Estimate{Delta: 0.5, Low: 0.4, High: 0.6},
				DriftB:   DriftReport{N: 101, PValue: 0.0001, RelativeShift: -0.07},
			},
			want: "candidate B drifted",
		},
		{
			name: "coarse measurement",
			report: Report{
				Validated: true, NoiseFloor: 0.01,
				Estimate:    Estimate{Delta: 0.5, Low: 0.4, High: 0.6},
				ValidationA: HarnessValidation{TieRate: 0.4, Level: 0.95},
				ValidationB: HarnessValidation{Level: 0.95},
			},
			want: "lower CollectOptions.MaxQuantizationError",
		},
		{
			name: "harness reports differences on identical code",
			report: Report{
				Validated: true, NoiseFloor: 0.01,
				Estimate:    Estimate{Delta: 0.5, Low: 0.4, High: 0.6},
				ValidationA: HarnessValidation{FalseSignalRate: 0.4, Level: 0.95},
				ValidationB: HarnessValidation{Level: 0.95},
			},
			want: "with suspicion",
		},
		{
			name: "nothing wrong",
			report: Report{
				Validated: true, NoiseFloor: 0.01,
				Estimate:    Estimate{Delta: 0.5, Low: 0.4, High: 0.6},
				ValidationA: HarnessValidation{Level: 0.95},
				ValidationB: HarnessValidation{Level: 0.95},
			},
			absent: "warning",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := strings.Join(c.report.warnings(), "\n")
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("warnings do not mention %q:\n%s", c.want, got)
			}
			if c.absent != "" && strings.Contains(got, c.absent) {
				t.Errorf("warnings should not mention %q:\n%s", c.absent, got)
			}
			if c.name == "nothing wrong" && len(c.report.warnings()) != 0 {
				t.Errorf("a clean report should carry no warnings, got %v", c.report.warnings())
			}
		})
	}
}
