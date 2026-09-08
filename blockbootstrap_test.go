package rtcompare

import (
	"math"
	"testing"
)

func TestAutoBlockLength(t *testing.T) {
	cases := map[int]int{0: 1, 1: 1, 8: 2, 11: 2, 27: 3, 51: 4, 101: 5, 1000: 10}
	for n, want := range cases {
		if got := AutoBlockLength(n); got != want {
			t.Errorf("AutoBlockLength(%d) = %d, want %d", n, got, want)
		}
	}
	if got := AutoBlockLength(-5); got != 1 {
		t.Errorf("AutoBlockLength(-5) = %d, want 1", got)
	}
}

func TestBlockSampleShapeAndContent(t *testing.T) {
	xs := make([]float64, 40)
	for i := range xs {
		xs[i] = float64(i)
	}
	rng := NewDPRNG(99)
	for _, L := range []int{1, 3, 7, 40, 100} {
		sample := blockSample(xs, L, rng.UInt32N)
		if len(sample) != len(xs) {
			t.Errorf("L=%d: replicate has %d values, want %d", L, len(sample), len(xs))
		}
		for _, v := range sample {
			if v < 0 || v >= 40 || v != math.Trunc(v) {
				t.Errorf("L=%d: value %v is not one of the inputs", L, v)
			}
		}
	}
	if got := blockSample(nil, 3, rng.UInt32N); len(got) != 0 {
		t.Errorf("empty input should give an empty replicate, got %v", got)
	}
}

func TestBlockSampleKeepsNeighboursTogether(t *testing.T) {
	// The point of blocks: values that were adjacent in the input stay adjacent
	// in the replicate. With consecutive integers as input, a block shows up as
	// a run of consecutive values.
	xs := make([]float64, 60)
	for i := range xs {
		xs[i] = float64(i)
	}
	rng := NewDPRNG(7)

	runsFor := func(L int) float64 {
		total, consecutive := 0, 0
		for range 200 {
			s := blockSample(xs, L, rng.UInt32N)
			for i := range len(s) - 1 {
				total++
				if s[i+1] == s[i]+1 {
					consecutive++
				}
			}
		}
		return float64(consecutive) / float64(total)
	}

	single, blocked := runsFor(1), runsFor(6)
	// With L=1 adjacency happens only by chance, about 1/60 of the time. With
	// L=6, five of every six steps are inside a block.
	if single > 0.1 {
		t.Errorf("single-observation sampling should rarely keep neighbours adjacent, got %.3f", single)
	}
	if blocked < 0.6 {
		t.Errorf("block sampling should keep neighbours adjacent most of the time, got %.3f", blocked)
	}
}

func TestBlockBootstrapWithLengthOneMatchesPlain(t *testing.T) {
	// Block length one is single-observation resampling. The two must agree
	// exactly, which is what lets them share an implementation.
	rng := NewDPRNG(2024)
	a := make([]float64, 51)
	b := make([]float64, 51)
	for i := range a {
		a[i] = 100 + rng.Float64()*10
		b[i] = 104 + rng.Float64()*10
	}
	gains := []float64{-0.02, 0.0, 0.03}
	for _, seed := range []uint64{1, 42, 12345} {
		plain := BootstrapConfidence(a, b, gains, 2000, seed)
		blocked := BlockBootstrapConfidence(a, b, gains, 2000, 1, seed)
		for _, g := range gains {
			if plain[g] != blocked[g] {
				t.Errorf("seed %d, threshold %v: plain %v vs block-of-one %v", seed, g, plain[g], blocked[g])
			}
		}
	}
}

func TestBlockBootstrapZeroLengthUsesAuto(t *testing.T) {
	rng := NewDPRNG(555)
	a := make([]float64, 101)
	b := make([]float64, 101)
	for i := range a {
		a[i] = 100 + rng.Float64()*10
		b[i] = 104 + rng.Float64()*10
	}
	gains := []float64{0.0}
	auto := BlockBootstrapConfidence(a, b, gains, 3000, 0, 77)
	explicit := BlockBootstrapConfidence(a, b, gains, 3000, AutoBlockLength(101), 77)
	if auto[0.0] != explicit[0.0] {
		t.Errorf("length zero should equal AutoBlockLength(%d)=%d, got %v vs %v",
			101, AutoBlockLength(101), auto[0.0], explicit[0.0])
	}
}

func TestBlockBootstrapAgreesOnSeparatedData(t *testing.T) {
	// Blocks must not change the answer when there is nothing subtle going on.
	a, b := sampleAB()
	res := BlockBootstrapConfidence(a, b, []float64{0.0, 0.4}, 1000, 0, 42)
	if res[0.0] != 1.0 {
		t.Errorf("A is always faster, expected confidence 1 at threshold 0, got %v", res[0.0])
	}
	if res[0.4] != 1.0 {
		t.Errorf("A is exactly 50%% faster, expected confidence 1 at threshold 0.4, got %v", res[0.4])
	}
}

// ar1Series generates an AR(1) series with the given lag-1 correlation, holding
// the marginal variance fixed so that only the dependence changes.
func ar1Series(rng *DPRNG, n int, rho float64) []float64 {
	s := make([]float64, n)
	sd := math.Sqrt(1 - rho*rho)
	gauss := func() float64 {
		u1 := rng.Float64()
		if u1 < 1e-12 {
			u1 = 1e-12
		}
		return math.Sqrt(-2*math.Log(u1)) * math.Cos(2*math.Pi*rng.Float64())
	}
	x := gauss()
	for i := range s {
		x = rho*x + sd*gauss()
		s[i] = 100 + 4*x
	}
	return s
}

func TestBlockBootstrapImprovesCalibrationUnderDependence(t *testing.T) {
	// With identical inputs every reported difference is a false signal, and a
	// calibrated method produces them 10% of the time at this band. Strong
	// dependence makes the plain bootstrap far exceed that; blocks pull it back.
	const (
		trials    = 600
		resamples = 800
		n         = 101
		rho       = 0.5
	)
	rate := func(useBlocks bool) float64 {
		rng := NewDPRNG(0xCAFE)
		out := 0
		for range trials {
			a := ar1Series(&rng, n, rho)
			b := ar1Series(&rng, n, rho)
			var c float64
			if useBlocks {
				c = BlockBootstrapConfidence(a, b, []float64{0.0}, resamples, 0, 0)[0.0]
			} else {
				c = BootstrapConfidence(a, b, []float64{0.0}, resamples, 0)[0.0]
			}
			if c < 0.05 || c > 0.95 {
				out++
			}
		}
		return float64(out) / trials
	}

	plain, blocked := rate(false), rate(true)
	t.Logf("rho=%.2f: plain %.1f%%, blocks %.1f%% (nominal 10%%)", rho, plain*100, blocked*100)

	if plain <= 0.15 {
		t.Errorf("expected the plain bootstrap to be clearly inflated at rho=%.2f, got %.3f", rho, plain)
	}
	if blocked >= plain {
		t.Errorf("blocks should reduce the false signal rate: plain %.3f, blocks %.3f", plain, blocked)
	}
}

func TestBlockBootstrapDoesNoHarmWithoutDependence(t *testing.T) {
	// Blocks must not make matters worse on independent samples.
	const (
		trials    = 600
		resamples = 800
		n         = 101
	)
	rng := NewDPRNG(0xFEED)
	out := 0
	for range trials {
		a := ar1Series(&rng, n, 0)
		b := ar1Series(&rng, n, 0)
		c := BlockBootstrapConfidence(a, b, []float64{0.0}, resamples, 0, 0)[0.0]
		if c < 0.05 || c > 0.95 {
			out++
		}
	}
	rate := float64(out) / trials
	t.Logf("rho=0: blocks %.1f%% (nominal 10%%)", rate*100)
	if rate > 0.16 {
		t.Errorf("blocks inflated the false signal rate on independent samples: %.3f", rate)
	}
}

func TestLag1Autocorrelation(t *testing.T) {
	if got := lag1Autocorrelation([]float64{1}); got != 0 {
		t.Errorf("a single value has no lag-1 correlation, got %v", got)
	}
	constant := []float64{5, 5, 5, 5}
	if got := lag1Autocorrelation(constant); got != 0 {
		t.Errorf("a constant series has no variation to correlate, got %v", got)
	}
	// A strongly dependent series must show it; an alternating one must be
	// strongly negative.
	rng := NewDPRNG(4242)
	if got := lag1Autocorrelation(ar1Series(&rng, 4000, 0.7)); got < 0.6 || got > 0.8 {
		t.Errorf("expected lag-1 near 0.7 for an AR(1) with rho=0.7, got %v", got)
	}
	alternating := make([]float64, 100)
	for i := range alternating {
		alternating[i] = float64(i%2)*2 - 1
	}
	if got := lag1Autocorrelation(alternating); got > -0.9 {
		t.Errorf("expected a strongly negative lag-1 for an alternating series, got %v", got)
	}
}
