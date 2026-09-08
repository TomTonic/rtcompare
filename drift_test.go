package rtcompare

import (
	"math"
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
		rng := NewDPRNG(0xABCDEF)
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
	rng := NewDPRNG(12345)
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
