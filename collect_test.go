package rtcompare

import (
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"testing"
)

// collectSink absorbs results of measured work so the compiler cannot eliminate it.
var collectSink uint64

// noopCandidate returns a candidate that does nothing, for option-validation tests.
func noopCandidate() Candidate {
	return Candidate{Batch: func(n uint64) {}}
}

// recorder builds a pair of candidates that append their label to a shared log
// every time they are invoked, so tests can assert on measurement order.
func recorder() (log *[]string, a, b Candidate) {
	l := make([]string, 0, 256)
	log = &l
	a = Candidate{Name: "A", Batch: func(n uint64) { *log = append(*log, "A") }}
	b = Candidate{Name: "B", Batch: func(n uint64) { *log = append(*log, "B") }}
	return log, a, b
}

func TestCollectRejectsNilBatch(t *testing.T) {
	opt := CollectOptions{InnerLoops: 1}
	_, _, err := Collect(Candidate{Name: "mine"}, noopCandidate(), opt)
	if err == nil {
		t.Fatal("expected error for nil Batch in candidate A, got nil")
	}
	if !strings.Contains(err.Error(), `A ("mine")`) {
		t.Errorf("error should identify the candidate by position and name, got %q", err.Error())
	}
	if _, _, err := Collect(noopCandidate(), Candidate{}, opt); err == nil {
		t.Error("expected error for nil Batch in candidate B, got nil")
	}
}

func TestCollectRejectsBadOptions(t *testing.T) {
	cases := []struct {
		name string
		opt  CollectOptions
		want string
	}{
		{"negative Repeats", CollectOptions{Repeats: -1, InnerLoops: 1}, "Repeats must not be negative"},
		{"too few Repeats", CollectOptions{Repeats: 10, InnerLoops: 1}, "at least"},
		{"negative Warmup", CollectOptions{Repeats: 11, InnerLoops: 1, Warmup: -1}, "SkipWarmup"},
		{"unknown Order", CollectOptions{Repeats: 11, InnerLoops: 1, Order: Order(42)}, "Order"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := Collect(noopCandidate(), noopCandidate(), c.opt)
			if err == nil {
				t.Fatalf("expected an error, got nil")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not mention %q", err.Error(), c.want)
			}
		})
	}
}

func TestCollectRepeatsDefaultAndExplicit(t *testing.T) {
	a, b, err := Collect(noopCandidate(), noopCandidate(), CollectOptions{InnerLoops: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a) != DefaultRepeats || len(b) != DefaultRepeats {
		t.Errorf("expected %d samples each, got %d and %d", DefaultRepeats, len(a), len(b))
	}

	a, b, err = Collect(noopCandidate(), noopCandidate(), CollectOptions{Repeats: 17, InnerLoops: 1})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(a) != 17 || len(b) != 17 {
		t.Errorf("expected 17 samples each, got %d and %d", len(a), len(b))
	}
}

func TestCollectPassesInnerLoopsToBatch(t *testing.T) {
	const want = uint64(1234)
	seen := make(map[uint64]int)
	c := Candidate{Batch: func(n uint64) { seen[n]++ }}
	if _, _, err := Collect(c, c, CollectOptions{Repeats: 11, InnerLoops: want}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(seen) != 1 {
		t.Fatalf("batch was called with varying n: %v", seen)
	}
	if _, ok := seen[want]; !ok {
		t.Errorf("batch never received n=%d, saw %v", want, seen)
	}
}

func TestCollectOrderABBAAlternates(t *testing.T) {
	log, a, b := recorder()
	_, _, err := Collect(a, b, CollectOptions{Repeats: 12, InnerLoops: 1, Order: OrderABBA, SkipWarmup: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := strings.Join(*log, "")
	want := "ABBAABBAABBAABBAABBAABBA" // 12 repeats, order flipping every repeat
	if got != want {
		t.Errorf("ABBA order mismatch:\n got %s\nwant %s", got, want)
	}
}

func TestCollectOrderSequentialAlwaysAFirst(t *testing.T) {
	log, a, b := recorder()
	_, _, err := Collect(a, b, CollectOptions{Repeats: 11, InnerLoops: 1, Order: OrderSequential, SkipWarmup: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for i := 0; i < len(*log); i += 2 {
		if (*log)[i] != "A" || (*log)[i+1] != "B" {
			t.Fatalf("expected strict A,B pairs, got %v at index %d", (*log)[i:i+2], i)
		}
	}
}

func TestCollectOrderRandomIsReproducibleBySeed(t *testing.T) {
	run := func(seed uint64) string {
		log, a, b := recorder()
		_, _, err := Collect(a, b, CollectOptions{
			Repeats: 51, InnerLoops: 1, Order: OrderRandom, Seed: seed, SkipWarmup: true,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return strings.Join(*log, "")
	}
	first, second := run(0xC0FFEE), run(0xC0FFEE)
	if first != second {
		t.Errorf("same seed produced different orders:\n%s\n%s", first, second)
	}
	if other := run(0xBEEF); other == first {
		t.Error("different seeds produced identical orders, seed appears to be ignored")
	}
	if !strings.Contains(first, "BA") {
		t.Error("random order never put B first, does not look randomized")
	}
}

func TestCollectWarmupCounts(t *testing.T) {
	count := func() (*int, Candidate) {
		n := 0
		return &n, Candidate{Batch: func(uint64) { n++ }}
	}

	// Warmup: 0 selects DefaultWarmup, so each candidate runs Repeats+DefaultWarmup times.
	na, ca := count()
	nb, cb := count()
	if _, _, err := Collect(ca, cb, CollectOptions{Repeats: 11, InnerLoops: 1}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := 11 + DefaultWarmup; *na != want || *nb != want {
		t.Errorf("default warm-up: expected %d calls each, got %d and %d", want, *na, *nb)
	}

	// Explicit warm-up count.
	na, ca = count()
	nb, cb = count()
	if _, _, err := Collect(ca, cb, CollectOptions{Repeats: 11, InnerLoops: 1, Warmup: 3}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *na != 14 || *nb != 14 {
		t.Errorf("warm-up 3: expected 14 calls each, got %d and %d", *na, *nb)
	}

	// SkipWarmup wins over an explicit count.
	na, ca = count()
	nb, cb = count()
	if _, _, err := Collect(ca, cb, CollectOptions{Repeats: 11, InnerLoops: 1, Warmup: 3, SkipWarmup: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if *na != 11 || *nb != 11 {
		t.Errorf("SkipWarmup: expected 11 calls each, got %d and %d", *na, *nb)
	}
}

func TestCollectSetupTeardownOrderAndCount(t *testing.T) {
	var log []string
	c := Candidate{
		Setup:    func() { log = append(log, "setup") },
		Batch:    func(uint64) { log = append(log, "batch") },
		Teardown: func() { log = append(log, "teardown") },
	}
	other := noopCandidate()

	const repeats = 11
	if _, _, err := Collect(c, other, CollectOptions{Repeats: repeats, InnerLoops: 1, SkipWarmup: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(log) != 3*repeats {
		t.Fatalf("expected %d lifecycle events, got %d", 3*repeats, len(log))
	}
	for i := 0; i < len(log); i += 3 {
		got := strings.Join(log[i:i+3], ",")
		if got != "setup,batch,teardown" {
			t.Fatalf("lifecycle out of order at event %d: %s", i, got)
		}
	}
}

func TestCollectSetupTeardownRunDuringWarmup(t *testing.T) {
	setups, teardowns := 0, 0
	c := Candidate{
		Setup:    func() { setups++ },
		Batch:    func(uint64) {},
		Teardown: func() { teardowns++ },
	}
	if _, _, err := Collect(c, noopCandidate(), CollectOptions{Repeats: 11, InnerLoops: 1, Warmup: 2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := 13; setups != want || teardowns != want {
		t.Errorf("warm-up should exercise the same lifecycle: expected %d setups/teardowns, got %d/%d", want, setups, teardowns)
	}
}

func TestCollectSetupWorkIsNotMeasured(t *testing.T) {
	// A candidate whose Setup burns time while its Batch does nothing must not
	// have that time attributed to it.
	burn := func() {
		rng := NewDPRNG(0x99)
		var acc uint64
		for range 200_000 {
			acc ^= rng.Uint64()
		}
		collectSink ^= acc
	}
	withSetup := Candidate{Name: "setup-heavy", Setup: burn, Batch: func(uint64) {}}
	plain := Candidate{Name: "plain", Batch: func(uint64) {}}

	sa, sb, err := Collect(withSetup, plain, CollectOptions{Repeats: 21, InnerLoops: 1000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	medSetup, medPlain := Median(sa), Median(sb)

	// Both batches are empty, so both medians must stay near zero. The burn loop
	// takes well over a microsecond; if it were measured it would dominate.
	if medSetup > medPlain+1.0 {
		t.Errorf("Setup work leaked into the measurement: setup-heavy=%.4f ns/op, plain=%.4f ns/op", medSetup, medPlain)
	}
}

func TestCollectDisableGCRestoresPreviousSetting(t *testing.T) {
	before := debug.SetGCPercent(123)
	defer debug.SetGCPercent(before)

	_, _, err := Collect(noopCandidate(), noopCandidate(), CollectOptions{
		Repeats: 11, InnerLoops: 1, DisableGC: true, GCBetween: true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	restored := debug.SetGCPercent(123)
	if restored != 123 {
		t.Errorf("DisableGC did not restore the previous GC percentage, found %d", restored)
	}
}

func TestCollectDisableGCIsOffDuringRun(t *testing.T) {
	var during int
	probe := Candidate{Batch: func(uint64) {
		// SetGCPercent returns the current value; -1 means the collector is off.
		current := debug.SetGCPercent(-1)
		during = current
	}}
	before := debug.SetGCPercent(200)
	defer debug.SetGCPercent(before)

	if _, _, err := Collect(probe, noopCandidate(), CollectOptions{
		Repeats: 11, InnerLoops: 1, DisableGC: true, SkipWarmup: true,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if during != -1 {
		t.Errorf("expected the collector to be disabled inside the run, saw GC percent %d", during)
	}
}

func TestCollectProducesUsableSamples(t *testing.T) {
	work := func(mult uint64) Candidate {
		return Candidate{Batch: func(n uint64) {
			rng := NewDPRNG(0x12345)
			var acc uint64
			for range n * mult {
				acc ^= rng.Uint64()
			}
			collectSink ^= acc
		}}
	}

	sa, sb, err := Collect(work(1), work(10), CollectOptions{Repeats: 21, InnerLoops: 2000})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for i, v := range sa {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			t.Fatalf("sample A[%d] is not a positive finite duration: %v", i, v)
		}
	}
	for i, v := range sb {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			t.Fatalf("sample B[%d] is not a positive finite duration: %v", i, v)
		}
	}

	medFast, medSlow := Median(sa), Median(sb)
	if medFast >= medSlow {
		t.Errorf("candidate doing 1x work (%.2f ns/op) should beat 10x work (%.2f ns/op)", medFast, medSlow)
	}

	// The samples must be directly consumable by the comparison API.
	if _, err := CompareSamplesDefault(sa, sb, []float64{0.0, 0.5}); err != nil {
		t.Errorf("Collect output rejected by CompareSamplesDefault: %v", err)
	}
}

func TestOrderString(t *testing.T) {
	cases := map[Order]string{
		OrderABBA:       "ABBA",
		OrderRandom:     "Random",
		OrderSequential: "Sequential",
		Order(42):       "Order(42)",
	}
	for o, want := range cases {
		if got := o.String(); got != want {
			t.Errorf("Order(%d).String() = %q, want %q", int(o), got, want)
		}
	}
}

func TestOrderABBAIsZeroValue(t *testing.T) {
	var o Order
	if o != OrderABBA {
		t.Errorf("zero value of Order is %v, expected OrderABBA so the safe order is the default", o)
	}
}

func TestCandidateLabel(t *testing.T) {
	if got := (Candidate{}).label("A"); got != "A" {
		t.Errorf("unnamed candidate label = %q, want %q", got, "A")
	}
	if got := (Candidate{Name: "quick"}).label("B"); got != `B ("quick")` {
		t.Errorf("named candidate label = %q, want %q", got, `B ("quick")`)
	}
}

// TestCollectResolvesBelowTheClock guards the central claim of this package:
// that a per-operation difference far smaller than one tick of the system clock
// is recovered, and recovered with the right magnitude.
//
// The two candidates run the same loop body, one over n units and the other over
// 2n. That construction is what makes the truth known: B's measured region is
// exactly twice A's, including the loop overhead, so the true relative
// difference is exactly 0.5 and attenuation cannot shrink it. A candidate pair
// differing by a called function would not have that property, which is why the
// test does not use one.
func TestCollectResolvesBelowTheClock(t *testing.T) {
	units := func(mult uint64) Candidate {
		return Candidate{Name: fmt.Sprintf("x%d", mult), Batch: func(n uint64) {
			var acc uint64
			for i := uint64(0); i < n*mult; i++ {
				acc = acc*31 + i
			}
			collectSink ^= acc
		}}
	}

	cal, err := CalibrateInnerLoops(units(1), CalibrationOptions{GCBetween: true})
	if err != nil {
		t.Fatalf("calibration failed: %v", err)
	}
	opts := CollectOptions{GCBetween: true, InnerLoops: cal.InnerLoops}

	// Three runs, judged by the middle one, so a single disturbed run on a busy
	// machine cannot fail the build.
	deltas := make([]float64, 0, 3)
	var ma, mb float64
	for range 3 {
		sa, sb, err := Collect(units(1), units(2), opts)
		if err != nil {
			t.Fatalf("Collect failed: %v", err)
		}
		ma, mb = Median(sa), Median(sb)
		deltas = append(deltas, 1-ma/mb)
	}
	delta := Median(deltas)

	tick := float64(cal.ClockPrecision)
	t.Logf("clock %.0f ns, %d inner loops, batch %.0f ns, quantization %.4f%%",
		tick, cal.InnerLoops, cal.BatchDuration, cal.QuantizationError*100)
	t.Logf("A %.4f ns/op, B %.4f ns/op, difference %.4f ns, measured delta %.4f (true 0.5)",
		ma, mb, mb-ma, delta)

	// The magnitude has to come out right regardless of how fast the machine is.
	const truth = 0.5
	if math.Abs(delta-truth) > 0.05 {
		t.Errorf("measured relative difference %.4f is not within 0.05 of the true %.2f", delta, truth)
	}

	// The rest of the test is the claim about the clock, and it only means
	// something while one operation is genuinely too cheap to time directly.
	if mb >= tick {
		t.Skipf("one operation of the slower candidate costs %.2f ns, which a %.0f ns clock resolves directly; "+
			"the sub-resolution claim needs a faster machine or a cheaper operation", mb, tick)
	}
	if diff := mb - ma; diff >= tick {
		t.Errorf("the difference being resolved, %.4f ns, is not below one clock tick of %.0f ns", diff, tick)
	}
	t.Logf("resolved a difference of %.4f ns using a clock that ticks every %.0f ns, a factor of %.0f",
		mb-ma, tick, tick/(mb-ma))
}

// calibSink absorbs measured work so the compiler cannot eliminate it.
var calibSink uint64

// spin performs cost units of cheap work per operation, so tests can build
// candidates of controlled expense that the compiler cannot remove.
func spinCandidate(cost uint64) Candidate {
	return Candidate{
		Name: "spin",
		Batch: func(n uint64) {
			rng := NewDPRNG(0x12345)
			var acc uint64
			for range n * cost {
				acc ^= rng.Uint64()
			}
			calibSink ^= acc
		},
	}
}

func TestCalibrateRejectsNilBatch(t *testing.T) {
	_, err := CalibrateInnerLoops(Candidate{Name: "mine"}, CalibrationOptions{})
	if err == nil {
		t.Fatal("expected an error for a nil Batch, got nil")
	}
	if !strings.Contains(err.Error(), `"mine"`) {
		t.Errorf("error should name the candidate, got %q", err.Error())
	}
}

func TestCalibrateRejectsBadQuantizationError(t *testing.T) {
	for _, v := range []float64{-0.1, 1.0, 2.5, math.NaN()} {
		_, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{MaxQuantizationError: v})
		if err == nil {
			t.Errorf("expected an error for MaxQuantizationError=%v, got nil", v)
			continue
		}
		if !strings.Contains(err.Error(), "MaxQuantizationError") {
			t.Errorf("error for %v does not mention the field: %q", v, err.Error())
		}
	}
}

func TestCalibrateMeetsRequestedQuantizationError(t *testing.T) {
	for _, target := range []float64{0.01, 0.001} {
		cal, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{MaxQuantizationError: target})
		if err != nil {
			t.Fatalf("target %v: unexpected error: %v", target, err)
		}
		if cal.InnerLoops == 0 {
			t.Fatalf("target %v: calibration returned zero InnerLoops", target)
		}
		if cal.QuantizationError > target {
			t.Errorf("target %v: achieved quantization error %v exceeds it", target, cal.QuantizationError)
		}
		// The batch must actually be long enough for the requested precision.
		wantNs := float64(cal.ClockPrecision) / target
		if cal.BatchDuration < wantNs {
			t.Errorf("target %v: batch of %.0f ns is short of the required %.0f ns", target, cal.BatchDuration, wantNs)
		}
		// Self-consistency of the reported fields.
		if got := cal.NsPerOp * float64(cal.InnerLoops); math.Abs(got-cal.BatchDuration) > 1 {
			t.Errorf("target %v: NsPerOp*InnerLoops = %.2f does not match BatchDuration %.2f", target, got, cal.BatchDuration)
		}
	}
}

func TestCalibrateTighterTargetNeedsLongerBatches(t *testing.T) {
	loose, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{MaxQuantizationError: 0.01})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	tight, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{MaxQuantizationError: 0.0005})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tight.InnerLoops <= loose.InnerLoops {
		t.Errorf("a 20x tighter target should need more operations per batch, got %d vs %d",
			tight.InnerLoops, loose.InnerLoops)
	}
}

func TestCalibrateCheaperOperationNeedsMoreLoops(t *testing.T) {
	cheap, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expensive, err := CalibrateInnerLoops(spinCandidate(200), CalibrationOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cheap.InnerLoops <= expensive.InnerLoops {
		t.Errorf("the cheaper operation should need the larger batch, got cheap=%d expensive=%d",
			cheap.InnerLoops, expensive.InnerLoops)
	}
	if cheap.NsPerOp >= expensive.NsPerOp {
		t.Errorf("the cheaper operation should measure less per op, got cheap=%.2f expensive=%.2f",
			cheap.NsPerOp, expensive.NsPerOp)
	}
}

func TestCalibrateDetectsBatchThatIgnoresN(t *testing.T) {
	// A batch whose duration does not grow with n can never reach the target.
	// The error must name that as the leading explanation.
	empty := Candidate{Name: "empty", Batch: func(n uint64) {}}
	_, err := CalibrateInnerLoops(empty, CalibrationOptions{MaxInnerLoops: 4096})
	if err == nil {
		t.Fatal("expected calibration of an empty batch to fail, got nil")
	}
	if !strings.Contains(err.Error(), "ignores its n parameter") {
		t.Errorf("error should point at a batch that ignores n, got %q", err.Error())
	}
}

func TestCalibrateSurvivesUnusedResult(t *testing.T) {
	// The Go compiler does not delete a loop just because nothing reads its
	// result, so a candidate that discards its accumulator must still calibrate
	// normally rather than trip the "batch does not scale" error. This guards
	// the claim made in the CalibrateInnerLoops documentation.
	discarding := Candidate{Name: "discarding", Batch: func(n uint64) {
		rng := NewDPRNG(0x1)
		var acc uint64
		for range n {
			acc ^= rng.Uint64()
		}
		_ = acc
	}}
	cal, err := CalibrateInnerLoops(discarding, CalibrationOptions{MaxQuantizationError: 0.01})
	if err != nil {
		t.Fatalf("a loop with an unused result should still be measurable: %v", err)
	}
	if cal.NsPerOp <= 0 {
		t.Errorf("expected a positive per-operation cost, got %v", cal.NsPerOp)
	}
}

func TestCalibrateRespectsMaxInnerLoops(t *testing.T) {
	// A very cheap operation with a low cap must fail rather than run away.
	_, err := CalibrateInnerLoops(spinCandidate(1), CalibrationOptions{MaxInnerLoops: 8})
	if err == nil {
		t.Fatal("expected calibration to fail when capped below the needed batch size")
	}
	if !strings.Contains(err.Error(), "calibration failed") {
		t.Errorf("unexpected error text: %q", err.Error())
	}
}

func TestGrowInnerLoopsAlwaysMakesProgress(t *testing.T) {
	cases := []struct {
		n      uint64
		factor float64
		limit  uint64
	}{
		{1, 1.0, 1000},
		{1, 0.5, 1000},
		{1, math.NaN(), 1000},
		{10, 1.01, 1000},
		{10, 1e9, 1000},
		{999, 100, 1000},
	}
	for _, c := range cases {
		got := growInnerLoops(c.n, c.factor, c.limit)
		if got <= c.n && c.n < c.limit {
			t.Errorf("growInnerLoops(%d, %v, %d) = %d, must exceed n to make progress", c.n, c.factor, c.limit, got)
		}
		if got > c.limit {
			t.Errorf("growInnerLoops(%d, %v, %d) = %d, must not exceed the limit", c.n, c.factor, c.limit, got)
		}
	}
}

func TestGrowInnerLoopsClampsToLimit(t *testing.T) {
	if got := growInnerLoops(1000, 100, 1000); got != 1000 {
		t.Errorf("at the limit growInnerLoops should return the limit, got %d", got)
	}
	// Guard against overflow when n is already enormous.
	if got := growInnerLoops(math.MaxUint64/2, 100, DefaultMaxInnerLoops); got != DefaultMaxInnerLoops {
		t.Errorf("expected clamping to the limit, got %d", got)
	}
}

func TestCollectAutoCalibratesInnerLoops(t *testing.T) {
	// InnerLoops left at zero must calibrate rather than fail.
	sa, sb, err := Collect(spinCandidate(1), spinCandidate(3), CollectOptions{
		Repeats:              21,
		MaxQuantizationError: 0.01, // loose, to keep the test quick
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sa) != 21 || len(sb) != 21 {
		t.Fatalf("expected 21 samples each, got %d and %d", len(sa), len(sb))
	}
	for i, v := range sa {
		if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
			t.Fatalf("sample A[%d] is not a positive finite duration: %v", i, v)
		}
	}
	medA, medB := Median(sa), Median(sb)
	if medA >= medB {
		t.Errorf("1x work (%.2f ns/op) should beat 3x work (%.2f ns/op)", medA, medB)
	}
}

func TestCollectPropagatesCalibrationFailure(t *testing.T) {
	empty := Candidate{Name: "empty", Batch: func(n uint64) {}}
	_, _, err := Collect(empty, spinCandidate(1), CollectOptions{Repeats: 11, MaxInnerLoops: 4096})
	if err == nil {
		t.Fatal("expected Collect to fail when a candidate cannot be calibrated")
	}
	if !strings.Contains(err.Error(), `A ("empty")`) {
		t.Errorf("error should identify which candidate failed calibration, got %q", err.Error())
	}
}

func TestCollectUsesTheLargerCalibratedBatch(t *testing.T) {
	// Both candidates must receive the same n, and it must be the one the
	// cheaper candidate needs.
	var seenA, seenB uint64
	cheap := Candidate{Name: "cheap", Batch: func(n uint64) {
		seenA = n
		rng := NewDPRNG(0x1)
		var acc uint64
		for range n {
			acc ^= rng.Uint64()
		}
		calibSink ^= acc
	}}
	expensive := Candidate{Name: "expensive", Batch: func(n uint64) {
		seenB = n
		rng := NewDPRNG(0x1)
		var acc uint64
		for range n * 100 {
			acc ^= rng.Uint64()
		}
		calibSink ^= acc
	}}

	if _, _, err := Collect(cheap, expensive, CollectOptions{
		Repeats: 11, MaxQuantizationError: 0.01,
	}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenA != seenB {
		t.Errorf("both candidates must run the same batch size, got %d and %d", seenA, seenB)
	}

	solo, err := CalibrateInnerLoops(cheap, CalibrationOptions{MaxQuantizationError: 0.01})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if seenA < solo.InnerLoops {
		t.Errorf("batch size %d is below what the cheaper candidate needs alone (%d)", seenA, solo.InnerLoops)
	}
}
