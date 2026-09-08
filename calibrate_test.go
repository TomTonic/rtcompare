package rtcompare

import (
	"math"
	"strings"
	"testing"
)

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
