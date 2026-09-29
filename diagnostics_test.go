package rtcompare

import (
	"github.com/TomTonic/rtcompare/prng"
	"math"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

func TestDetectDriftRejectsBadInput(t *testing.T) {
	if _, err := DetectDrift([]float64{1, 2, 3}); err == nil {
		t.Error("expected an error for fewer than four samples")
	}
	if _, err := DetectDrift([]float64{1, 2, math.NaN(), 4}); err == nil {
		t.Error("expected an error for a NaN sample")
	}
	if _, err := DetectDrift([]float64{1, 2, math.Inf(1), 4}); err == nil {
		t.Error("expected an error for an infinite sample")
	}
}

func TestDetectDriftFindsMonotoneTrends(t *testing.T) {
	rising := make([]float64, 60)
	falling := make([]float64, 60)
	for i := range rising {
		rising[i] = float64(i)
		falling[i] = float64(60 - i)
	}

	up, err := DetectDrift(rising)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if up.Spearman < 0.99 {
		t.Errorf("a strictly increasing series should give rho near 1, got %v", up.Spearman)
	}
	if !up.Drifted(0.001) {
		t.Errorf("a strictly increasing series should be significant, p=%v", up.PValue)
	}
	if up.SecondHalf <= up.FirstHalf {
		t.Errorf("second half (%v) should exceed the first (%v)", up.SecondHalf, up.FirstHalf)
	}

	down, err := DetectDrift(falling)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if down.Spearman > -0.99 {
		t.Errorf("a strictly decreasing series should give rho near -1, got %v", down.Spearman)
	}
	if down.RelativeShift >= 0 {
		t.Errorf("a decreasing series should give a negative shift, got %v", down.RelativeShift)
	}
}

func TestDetectDriftConstantSeries(t *testing.T) {
	constant := make([]float64, 40)
	for i := range constant {
		constant[i] = 7
	}
	d, err := DetectDrift(constant)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d.Spearman != 0 || d.Z != 0 {
		t.Errorf("a constant series has no trend to find, got rho=%v z=%v", d.Spearman, d.Z)
	}
	if d.PValue != 1 {
		t.Errorf("a constant series should give p=1, got %v", d.PValue)
	}
	if d.Drifted(0.05) {
		t.Error("a constant series must not be reported as drifting")
	}
}

func TestDetectDriftShiftIsHalfALinearTrend(t *testing.T) {
	// Split-half medians sit near the quarter and three-quarter points of a run,
	// so a linear trend of size d across it shows up as a shift of about d/2.
	//
	// "About" is a first-order approximation and the test stays inside its range
	// deliberately. Exactly, with n = 101, the medians are samples 25 and 76 of
	// 101, so the shift is 0.51*d/(1 + 0.25*d): the numerator is the separation
	// of the two medians and the denominator is the level the first one already
	// sits at. That correction is 1.5% of the value at d = 0.10 and 9.3% at
	// d = 0.50, which is why large trends are not tested against d/2.
	const n = 101
	for _, trend := range []float64{0.02, 0.05, 0.10} {
		s := make([]float64, n)
		for i := range s {
			s[i] = 100 * (1 + trend*float64(i)/float64(n-1))
		}
		d, err := DetectDrift(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := trend / 2
		if math.Abs(d.RelativeShift-want) > want*0.02 {
			t.Errorf("trend %v: expected a shift near %v, got %v", trend, want, d.RelativeShift)
		}
	}
}

func TestDetectDriftIsCalibratedUnderTies(t *testing.T) {
	// The false positive rate must hold on quantized data, where most samples
	// are tied. This is the case timing measurements actually produce.
	for _, levels := range []int{3, 8, 35} {
		rng := prng.NewDPRNG(0xABCDEF)
		const trials = 3000
		hits := 0
		for range trials {
			s := make([]float64, 101)
			for i := range s {
				s[i] = math.Floor(rng.Float64()*float64(levels)) + 100
			}
			d, err := DetectDrift(s)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if d.Drifted(0.05) {
				hits++
			}
		}
		rate := float64(hits) / trials
		// The nominal rate is 0.05 with a standard error of 0.004 here; the
		// band is wide enough to be stable but would catch a broken test.
		if rate < 0.02 || rate > 0.09 {
			t.Errorf("with %d distinct values the false positive rate was %.3f, outside [0.02, 0.09]", levels, rate)
		}
	}
}

func TestDetectDriftHasPower(t *testing.T) {
	// A 2% trend buried in 4% noise must be found most of the time at n=101.
	rng := prng.NewDPRNG(12345)
	const trials = 500
	hits := 0
	for range trials {
		s := make([]float64, 101)
		for i := range s {
			s[i] = (100 + rng.Float64()*4) * (1 + 0.02*float64(i)/100)
		}
		d, err := DetectDrift(s)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d.Drifted(0.05) {
			hits++
		}
	}
	if rate := float64(hits) / trials; rate < 0.9 {
		t.Errorf("expected a 2%% trend to be detected in at least 90%% of runs, got %.1f%%", rate*100)
	}
}

func TestMidranks(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want []float64
	}{
		{"distinct ascending", []float64{10, 20, 30}, []float64{1, 2, 3}},
		{"distinct unordered", []float64{30, 10, 20}, []float64{3, 1, 2}},
		{"one tied pair", []float64{10, 10, 30}, []float64{1.5, 1.5, 3}},
		{"all tied", []float64{5, 5, 5, 5}, []float64{2.5, 2.5, 2.5, 2.5}},
		{"tie in the middle", []float64{1, 4, 4, 4, 9}, []float64{1, 3, 3, 3, 5}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := midranks(c.in); !slices.Equal(got, c.want) {
				t.Errorf("midranks(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestDriftReportString(t *testing.T) {
	d := DriftReport{N: 51, Spearman: 0.42, Z: 2.97, PValue: 0.003, RelativeShift: 0.012}
	s := d.String()
	for _, want := range []string{"51 samples", "rho", "p 0.0030", "shift"} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

func TestDetectDriftOnCollectOutput(t *testing.T) {
	// The slices Collect returns are in repeat order, which is what makes them
	// valid input here.
	c := steadyCandidate(2)
	sa, sb, err := Collect(c, c, CollectOptions{Repeats: 51, InnerLoops: 5000, GCBetween: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for name, samples := range map[string][]float64{"A": sa, "B": sb} {
		d, err := DetectDrift(samples)
		if err != nil {
			t.Fatalf("candidate %s: unexpected error: %v", name, err)
		}
		if d.N != 51 {
			t.Errorf("candidate %s: expected 51 samples, got %d", name, d.N)
		}
		if d.PValue < 0 || d.PValue > 1 || math.IsNaN(d.PValue) {
			t.Errorf("candidate %s: p-value %v outside [0,1]", name, d.PValue)
		}
		t.Logf("candidate %s: %s", name, d)
	}
}

func TestEstimateDifferenceRejectsBadInput(t *testing.T) {
	a, b := sampleAB()
	short := make([]float64, MinimumDataPoints-1)

	if _, err := EstimateDifference(short, b, EstimateOptions{Level: 0.95, Resamples: 100}); err == nil {
		t.Error("expected an error for too few samples in A")
	}
	if _, err := EstimateDifference(a, short, EstimateOptions{Level: 0.95, Resamples: 100}); err == nil {
		t.Error("expected an error for too few samples in B")
	}
	for _, level := range []float64{-0.1, 1.0, 1.5, math.NaN()} {
		if _, err := EstimateDifference(a, b, EstimateOptions{Level: level, Resamples: 100}); err == nil {
			t.Errorf("expected an error for level %v", level)
		}
	}
}

func TestEstimateDifferenceDefaults(t *testing.T) {
	a, b := sampleAB()
	e, err := EstimateDifference(a, b, EstimateOptions{Level: 0, Resamples: 0})
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
	e, err := EstimateDifference(a, b, EstimateOptions{Level: 0.95, Resamples: 500})
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
	rng := prng.NewDPRNG(31)
	a := make([]float64, 40)
	b := make([]float64, 40)
	for i := range a {
		a[i] = 100 + rng.Float64()*20
		b[i] = 130 + rng.Float64()*20
	}
	origA, origB := slices.Clone(a), slices.Clone(b)

	if _, err := EstimateDifference(a, b, EstimateOptions{Level: 0.95, Resamples: 300}); err != nil {
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
	rng := prng.NewDPRNG(77)
	a := make([]float64, 60)
	b := make([]float64, 60)
	for i := range a {
		a[i] = 100 + rng.Float64()*30
		b[i] = 125 + rng.Float64()*30
	}
	var previousWidth float64
	for _, level := range []float64{0.50, 0.80, 0.95, 0.99} {
		e, err := EstimateDifference(a, b, EstimateOptions{Level: level, Resamples: 4000})
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
	rng := prng.NewDPRNG(2468)
	const n = 101
	a := make([]float64, n)
	b := make([]float64, n)
	for i := range a {
		a[i] = 100 * (1 + 0.05*(rng.Float64()-0.5))
		b[i] = 125 * (1 + 0.05*(rng.Float64()-0.5))
	}
	e, err := EstimateDifference(a, b, EstimateOptions{Level: 0.95, Resamples: 4000})
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
	rng := prng.NewDPRNG(0xC0FFEE)
	truth := 1 - 100.0/125.0
	covered := 0
	for range trials {
		a := make([]float64, n)
		b := make([]float64, n)
		for i := range a {
			a[i] = 100 * (1 + 0.16*(rng.Float64()-0.5))
			b[i] = 125 * (1 + 0.16*(rng.Float64()-0.5))
		}
		e, err := EstimateDifference(a, b, EstimateOptions{Level: level, Resamples: resamples})
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

// TestEstimateRatioReadsTheSameEitherWayRound checks the second form a
// difference is reported in. The relative difference 1 - A/B changes its
// magnitude when the roles are swapped; the ratio B/A has to be its exact
// counterpart, carry the interval's bounds over, and appear in the printed
// estimate.
func TestEstimateRatioReadsTheSameEitherWayRound(t *testing.T) {
	cases := []struct {
		name               string
		delta, low, high   float64
		ratio, rLow, rHigh float64
	}{
		{"gives 2 when B takes twice as long", 0.5, 0.4, 0.6, 2, 1 / 0.6, 2.5},
		{"gives 0.5 when B takes half as long", -1, -1.5, -0.5, 0.5, 0.4, 1 / 1.5},
		{"gives 1 for no difference", 0, -0.1, 0.1, 1, 1 / 1.1, 1 / 0.9},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Estimate{Delta: c.delta, Low: c.low, High: c.high, Level: 0.95}
			r, lo, hi := e.Ratio()
			if math.Abs(r-c.ratio) > 1e-12 || math.Abs(lo-c.rLow) > 1e-12 || math.Abs(hi-c.rHigh) > 1e-12 {
				t.Errorf("ratio %v [%v, %v], want %v [%v, %v]", r, lo, hi, c.ratio, c.rLow, c.rHigh)
			}
			if !strings.Contains(e.String(), "B/A") {
				t.Errorf("the printed estimate lacks the ratio: %s", e)
			}
		})
	}
}

// TestEstimateDifferenceIsReproducibleFromItsSeed checks that a seeded
// interval can be recomputed: the same seed gives the same estimate, and seed
// zero still works, drawing from cryptographic randomness.
func TestEstimateDifferenceIsReproducibleFromItsSeed(t *testing.T) {
	a := []float64{10, 11, 12, 10.5, 11.5, 12.5, 10.2, 11.2, 12.2, 10.7, 11.7, 12.7, 10.9}
	b := []float64{12, 13, 14, 12.5, 13.5, 14.5, 12.2, 13.2, 14.2, 12.7, 13.7, 14.7, 12.9}
	first, err1 := EstimateDifference(a, b, EstimateOptions{Resamples: 500, Seed: 7})
	second, err2 := EstimateDifference(a, b, EstimateOptions{Resamples: 500, Seed: 7})
	if err1 != nil || err2 != nil || first != second {
		t.Errorf("same seed, different estimates: %+v and %+v (%v, %v)", first, second, err1, err2)
	}
	if _, err := EstimateDifference(a, b, EstimateOptions{Resamples: 500}); err != nil {
		t.Errorf("unseeded estimate failed: %v", err)
	}
}

// commonModePairs simulates n pairs of batches whose true difference is 0.2,
// each pair disturbed by a factor common to both members with the given
// spread, and each member by 2% of its own.
func commonModePairs(rng *rand.Rand, n int, common float64) (a, b []float64) {
	for range n {
		m := math.Exp(common * rng.NormFloat64())
		a = append(a, 100*m*math.Exp(0.02*rng.NormFloat64()))
		b = append(b, 125*m*math.Exp(0.02*rng.NormFloat64()))
	}
	return a, b
}

// TestPairedEstimateCancelsCommonDisturbances checks why Compare pairs its
// measurements: a disturbance that hits both batches of a pair, such as the
// clock speed changing, cancels in their ratio. Simulated from a fixed seed
// with such disturbances, the paired interval has to cover the true
// difference at about its level and be far narrower than the unpaired one,
// which covers nearly always because it counts the common disturbance as
// noise; the paired point estimate is 1 - median(A[i]/B[i]).
func TestPairedEstimateCancelsCommonDisturbances(t *testing.T) {
	rng := rand.New(rand.NewPCG(122, 1))
	const truth, trials = 0.2, 600
	hits := 0
	var paired, unpaired float64
	for trial := range trials {
		a, b := commonModePairs(rng, 51, 0.05)
		p, err := EstimateDifference(a, b, EstimateOptions{Resamples: 500, Seed: uint64(trial + 1), Paired: true})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		u, err := EstimateDifference(a, b, EstimateOptions{Resamples: 500, Seed: uint64(trial + 1)})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if p.Low <= truth && truth <= p.High {
			hits++
		}
		paired += p.High - p.Low
		unpaired += u.High - u.Low
		if trial == 0 {
			q := make([]float64, len(a))
			for i := range a {
				q[i] = a[i] / b[i]
			}
			if want := 1 - Median(q); p.Delta != want {
				t.Errorf("paired point estimate %v, want 1 - median of the ratios %v", p.Delta, want)
			}
		}
	}
	if coverage := float64(hits) / trials; coverage < 0.93 || coverage > 0.975 {
		t.Errorf("paired coverage %.1f%%, want about 95%%", coverage*100)
	}
	if ratio := paired / unpaired; ratio > 0.5 {
		t.Errorf("paired intervals are %.0f%% as wide as unpaired ones, want at most half", ratio*100)
	}
}

// TestConfidencesForMatchesTheInterval checks that the two answers a
// comparison gives come from the same resampling: under the same options, each
// confidence is exactly the share of the interval's replicates that reach the
// threshold, and about the level's upper tail reach the interval's lower
// bound. Paired measurements of unequal length, NaN thresholds and too few
// measurements are errors.
func TestConfidencesForMatchesTheInterval(t *testing.T) {
	a, b := commonModePairs(rand.New(rand.NewPCG(122, 2)), 51, 0.05)
	opt := EstimateOptions{Resamples: 2000, Seed: 3, Paired: true}
	e, err := EstimateDifference(a, b, opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	c, err := ConfidencesFor(a, b, []float64{e.Low, e.BootstrapMedian}, opt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	reps := replicates(a, b, opt.Resamples, 1, opt.Seed, true)
	for _, th := range []float64{e.Low, e.BootstrapMedian} {
		reached := 0
		for _, d := range reps {
			if d >= th {
				reached++
			}
		}
		if want := float64(reached) / float64(len(reps)); confAt(c, th) != want {
			t.Errorf("confidence at %v is %v, but %v of the interval's replicates reach it", th, confAt(c, th), want)
		}
	}
	if atLow := confAt(c, e.Low); atLow < 0.97 {
		t.Errorf("confidence at the interval's lower bound %.3f, want about 0.975", atLow)
	}
	bad := []struct {
		name       string
		a, b       []float64
		thresholds []float64
		want       string
	}{
		{"returns error for pairs of unequal length", a, b[:40], nil, "as many"},
		{"returns error for a NaN threshold", a, b, []float64{math.NaN()}, "NaN"},
		{"returns error for too few measurements", a[:5], b[:5], nil, "not enough"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ConfidencesFor(c.a, c.b, c.thresholds, opt); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}
