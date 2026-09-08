package rtcompare

import (
	"math"
	"slices"
	"strings"
	"testing"
)

func TestEstimateDifferenceRejectsBadInput(t *testing.T) {
	a, b := sampleAB()
	short := make([]float64, MinimumDataPoints-1)

	if _, err := EstimateDifference(short, b, 0.95, 100); err == nil {
		t.Error("expected an error for too few samples in A")
	}
	if _, err := EstimateDifference(a, short, 0.95, 100); err == nil {
		t.Error("expected an error for too few samples in B")
	}
	for _, level := range []float64{-0.1, 1.0, 1.5, math.NaN()} {
		if _, err := EstimateDifference(a, b, level, 100); err == nil {
			t.Errorf("expected an error for level %v", level)
		}
	}
}

func TestEstimateDifferenceDefaults(t *testing.T) {
	a, b := sampleAB()
	e, err := EstimateDifference(a, b, 0, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Level != DefaultConfidenceLevel {
		t.Errorf("level zero should select %v, got %v", DefaultConfidenceLevel, e.Level)
	}
	if e.Resamples != DefaultResamples {
		t.Errorf("resamples zero should select %v, got %v", DefaultResamples, e.Resamples)
	}
}

func TestEstimateDifferencePointEstimate(t *testing.T) {
	// A constant 10 against a constant 20 is exactly a 50% reduction, and every
	// replicate agrees, so the interval collapses onto it.
	a, b := sampleAB()
	e, err := EstimateDifference(a, b, 0.95, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if e.Delta != 0.5 {
		t.Errorf("expected a point estimate of exactly 0.5, got %v", e.Delta)
	}
	if e.Low != 0.5 || e.High != 0.5 {
		t.Errorf("with no variation the interval should collapse, got [%v, %v]", e.Low, e.High)
	}
	if !e.Excludes(0) {
		t.Error("an interval at exactly 0.5 must exclude zero")
	}
}

func TestEstimateDifferenceDoesNotMutateInputs(t *testing.T) {
	// QuickMedian rearranges what it is given; the caller's measurements must
	// survive unchanged.
	rng := NewDPRNG(31)
	a := make([]float64, 40)
	b := make([]float64, 40)
	for i := range a {
		a[i] = 100 + rng.Float64()*20
		b[i] = 130 + rng.Float64()*20
	}
	origA, origB := slices.Clone(a), slices.Clone(b)

	if _, err := EstimateDifference(a, b, 0.95, 300); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(a, origA) {
		t.Error("EstimateDifference reordered its first argument")
	}
	if !slices.Equal(b, origB) {
		t.Error("EstimateDifference reordered its second argument")
	}
}

func TestEstimateDifferenceIntervalOrderingAndWidth(t *testing.T) {
	rng := NewDPRNG(77)
	a := make([]float64, 60)
	b := make([]float64, 60)
	for i := range a {
		a[i] = 100 + rng.Float64()*30
		b[i] = 125 + rng.Float64()*30
	}
	var previousWidth float64
	for _, level := range []float64{0.50, 0.80, 0.95, 0.99} {
		e, err := EstimateDifference(a, b, level, 4000)
		if err != nil {
			t.Fatalf("level %v: unexpected error: %v", level, err)
		}
		if e.Low > e.High {
			t.Errorf("level %v: interval is inverted: [%v, %v]", level, e.Low, e.High)
		}
		width := e.High - e.Low
		if width < previousWidth {
			t.Errorf("level %v: interval narrower (%v) than at the previous, lower level (%v)", level, width, previousWidth)
		}
		previousWidth = width
	}
}

func TestEstimateDifferenceRecoversAKnownDifference(t *testing.T) {
	// A 20% reduction, with enough samples that the interval should contain it.
	rng := NewDPRNG(2468)
	const n = 101
	a := make([]float64, n)
	b := make([]float64, n)
	for i := range a {
		a[i] = 100 * (1 + 0.05*(rng.Float64()-0.5))
		b[i] = 125 * (1 + 0.05*(rng.Float64()-0.5))
	}
	e, err := EstimateDifference(a, b, 0.95, 4000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	const truth = 0.2
	if math.Abs(e.Delta-truth) > 0.02 {
		t.Errorf("point estimate %v is far from the true %v", e.Delta, truth)
	}
	if e.Low > truth || e.High < truth {
		t.Errorf("interval [%v, %v] does not contain the true difference %v", e.Low, e.High, truth)
	}
	if !e.Excludes(0) {
		t.Errorf("a 20%% difference at n=%d should exclude zero, interval was [%v, %v]", n, e.Low, e.High)
	}
}

func TestEstimateDifferenceCoverageIsAtLeastNominal(t *testing.T) {
	// The headline property. Coverage was measured at 96 to 97% against a
	// nominal 95%, conservative rather than optimistic; this guards the
	// direction, which is what matters, with a band wide enough to be stable.
	const (
		trials    = 400
		resamples = 500
		n         = 51
		level     = 0.95
	)
	rng := NewDPRNG(0xC0FFEE)
	truth := 1 - 100.0/125.0
	covered := 0
	for range trials {
		a := make([]float64, n)
		b := make([]float64, n)
		for i := range a {
			a[i] = 100 * (1 + 0.16*(rng.Float64()-0.5))
			b[i] = 125 * (1 + 0.16*(rng.Float64()-0.5))
		}
		e, err := EstimateDifference(a, b, level, resamples)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if e.Low <= truth && truth <= e.High {
			covered++
		}
	}
	rate := float64(covered) / trials
	t.Logf("coverage %.1f%% over %d trials (nominal %.0f%%, standard error %.1f points)",
		rate*100, trials, level*100, math.Sqrt(level*(1-level)/trials)*100)
	// Three standard errors below nominal would mean the interval is genuinely
	// optimistic, which is the failure worth catching.
	if rate < 0.92 {
		t.Errorf("coverage %.3f is well below the nominal %v; the interval is optimistic", rate, level)
	}
}

func TestEstimateExcludes(t *testing.T) {
	cases := []struct {
		low, high, value float64
		want             bool
	}{
		{0.1, 0.3, 0.0, true},
		{0.1, 0.3, 0.2, false},
		{0.1, 0.3, 0.1, false},
		{-0.3, -0.1, 0.0, true},
		{-0.1, 0.2, 0.0, false},
	}
	for _, c := range cases {
		e := Estimate{Low: c.low, High: c.high}
		if got := e.Excludes(c.value); got != c.want {
			t.Errorf("Estimate[%v,%v].Excludes(%v) = %v, want %v", c.low, c.high, c.value, got, c.want)
		}
	}
}

func TestEstimateString(t *testing.T) {
	e := Estimate{Delta: 0.1234, Low: 0.05, High: 0.19, Level: 0.95}
	s := e.String()
	for _, want := range []string{"+12.34%", "+5.00%", "+19.00%", "95%"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

func TestQuantileOfSorted(t *testing.T) {
	xs := []float64{0, 1, 2, 3, 4}
	cases := map[float64]float64{0: 0, 0.25: 1, 0.5: 2, 0.75: 3, 1: 4, -1: 0, 2: 4}
	for p, want := range cases {
		if got := quantileOfSorted(xs, p); got != want {
			t.Errorf("quantileOfSorted(%v, %v) = %v, want %v", xs, p, got, want)
		}
	}
	// Interpolation between neighbours.
	if got := quantileOfSorted([]float64{0, 10}, 0.5); got != 5 {
		t.Errorf("expected interpolation to 5, got %v", got)
	}
	if got := quantileOfSorted(nil, 0.5); !math.IsNaN(got) {
		t.Errorf("empty input should give NaN, got %v", got)
	}
	if got := quantileOfSorted([]float64{7}, 0.9); got != 7 {
		t.Errorf("single value should be returned for any p, got %v", got)
	}
}

func TestRelativeDelta(t *testing.T) {
	cases := []struct{ a, b, want float64 }{
		{10, 20, 0.5},
		{20, 10, -1.0},
		{5, 5, 0},
		{0, 0, 0},
		{math.Inf(1), math.Inf(1), 0},
		{math.Inf(-1), math.Inf(-1), 0},
	}
	for _, c := range cases {
		if got := relativeDelta(c.a, c.b); got != c.want {
			t.Errorf("relativeDelta(%v, %v) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
	if !math.IsNaN(relativeDelta(math.NaN(), 1)) || !math.IsNaN(relativeDelta(1, math.NaN())) {
		t.Error("a NaN operand should give NaN")
	}
	// A zero denominator with a non-zero numerator is an infinity, which is
	// the honest answer and signals a batch shorter than one clock tick. It
	// must not be NaN, which would silently meet no threshold for the wrong
	// reason.
	if got := relativeDelta(1, 0); !math.IsInf(got, -1) {
		t.Errorf("relativeDelta(1, 0) = %v, want -Inf", got)
	}
	if got := relativeDelta(-1, 0); !math.IsInf(got, 1) {
		t.Errorf("relativeDelta(-1, 0) = %v, want +Inf", got)
	}
}
