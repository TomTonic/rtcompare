package rtcompare

import (
	"math"
	"math/rand"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"testing/quick"
)

func TestCompareRuntimesTooFewData(t *testing.T) {
	A := make([]float64, 10)
	B := make([]float64, 11)
	_, err := CompareSamples(A, B, []float64{0.1}, 1000)
	if err == nil {
		t.Errorf("Expected error for too few data points, got nil")
	}
}

func TestCompareRuntimesDefaultThreshold(t *testing.T) {
	A := make([]float64, 11)
	B := make([]float64, 11)
	for i := range A {
		A[i] = 100
		B[i] = 120
	}
	results, err := CompareSamples(A, B, nil, 1000)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].RelativeSpeedupSampleAvsSampleB != 0.0 {
		t.Errorf("Expected default threshold 0.0, got %+v", results)
	}
}

func TestCompareRuntimesConfidenceRange(t *testing.T) {
	A := make([]float64, 11)
	B := make([]float64, 11)
	for i := range A {
		A[i] = 100
		B[i] = 120
	}
	thresholds := []float64{0.1, 0.2, 0.3}
	results, err := CompareSamples(A, B, thresholds, 1000)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	for _, r := range results {
		if r.Confidence < 0.0 || r.Confidence > 1.0 {
			t.Errorf("Confidence out of bounds: %.3f", r.Confidence)
		}
	}
}

func TestCompareRuntimesConfidenceMonotonicity(t *testing.T) {
	A := make([]float64, 11)
	B := make([]float64, 11)
	for i := range A {
		A[i] = 100
		B[i] = 130 // A ist deutlich schneller
	}
	thresholds := []float64{0.1, 0.2, 0.3, 0.4}
	results, err := CompareSamples(A, B, thresholds, 1000)
	if err != nil {
		t.Fatalf("Unexpected error: %v", err)
	}
	for i := 1; i < len(results); i++ {
		if results[i].Confidence > results[i-1].Confidence+0.01 {
			t.Errorf("Confidence not decreasing: %.3f > %.3f", results[i].Confidence, results[i-1].Confidence)
		}
	}
}

func TestBootstrapSampleBasic(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5}
	sample := bootstrapSample(xs, 0)

	if len(sample) != len(xs) {
		t.Errorf("Expected length %d, got %d", len(xs), len(sample))
	}

	for _, v := range sample {
		found := slices.Contains(xs, v)
		if !found {
			t.Errorf("Sample contains unknown value: %v", v)
		}
	}
}

func TestBootstrapSampleDeterministic(t *testing.T) {
	xs := []float64{10, 20, 30, 40, 50, 60, 70}
	sample1 := bootstrapSample(xs, 42)
	sample2 := bootstrapSample(xs, 42)

	if !reflect.DeepEqual(sample1, sample2) {
		t.Errorf("Expected deterministic output, got different samples")
	}
}

func TestBootstrapSampleEmpty(t *testing.T) {
	xs := []float64{}
	sample := bootstrapSample(xs, 0)

	if len(sample) != 0 {
		t.Errorf("Expected empty sample, got length %d", len(sample))
	}
}

func TestBootstrapSampleSingleElement(t *testing.T) {
	xs := []float64{42}
	sample := bootstrapSample(xs, 0)

	if len(sample) != 1 || sample[0] != 42 {
		t.Errorf("Expected [42], got %v", sample)
	}
}

func TestBootstrapSampleDistribution(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}
	counts := map[float64]int{}
	N := 1_000_000

	for range N {
		sample := bootstrapSample(xs, 0)
		for _, v := range sample {
			counts[v]++
		}
	}

	min := math.MaxInt32
	max := -1
	for _, x := range xs {
		if counts[x] == 0 {
			t.Errorf("Value %v never appeared in bootstrap samples", x)
		}
		if counts[x] < min {
			min = counts[x]
		}
		if counts[x] > max {
			max = counts[x]
		}
	}
	if float64(max-min)/float64(N*len(xs)) > 0.002 {
		t.Errorf("Distribution of bootstrap samples is too uneven: min=%d, max=%d", min, max)
	}
}

func TestBootstrapConfidenceDeterministic(t *testing.T) {
	A := []float64{100, 101, 99, 98, 102}
	B := []float64{120, 118, 122, 119, 121}
	thresholds := []float64{0.1, 0.2}
	reps := uint64(1000)
	seed := uint64(42)

	conf1 := BootstrapConfidence(A, B, thresholds, reps, seed)
	conf2 := BootstrapConfidence(A, B, thresholds, reps, seed)

	if !reflect.DeepEqual(conf1, conf2) {
		t.Errorf("Expected deterministic output with same seed, got different results")
	}
}

func TestBootstrapConfidenceHighConfidence(t *testing.T) {
	A := []float64{100, 101, 99, 98, 102}
	B := []float64{150, 160, 155, 158, 152}
	thresholds := []float64{0.3}
	reps := uint64(1000)
	seed := uint64(123)

	conf := BootstrapConfidence(A, B, thresholds, reps, seed)

	if conf[0.3] < 0.95 {
		t.Errorf("Expected high confidence for 30%% speedup, got %.2f", conf[0.3])
	}
}

func TestBootstrapConfidenceLowConfidence(t *testing.T) {
	A := []float64{100, 101, 99, 98, 102}
	B := []float64{100, 101, 99, 98, 102}
	thresholds := []float64{0.1}
	reps := uint64(1000)
	seed := uint64(456)

	conf := BootstrapConfidence(A, B, thresholds, reps, seed)

	if conf[0.1] > 0.2 {
		t.Errorf("Expected low confidence for 10%% speedup, got %.2f", conf[0.1])
	}
}

func TestBootstrapConfidenceEmptyInput(t *testing.T) {
	A := []float64{}
	B := []float64{}
	thresholds := []float64{0.1}
	reps := uint64(100)
	seed := uint64(789)

	conf := BootstrapConfidence(A, B, thresholds, reps, seed)
	// With empty inputs the implementation uses NaN medians and comparisons
	// never succeed, therefore the confidence should be 0.0 for each threshold.
	for _, th := range thresholds {
		if v, ok := conf[th]; !ok {
			t.Fatalf("missing threshold %v in result", th)
		} else if v != 0.0 {
			t.Fatalf("expected confidence 0.0 for empty input, got %.6f", v)
		}
	}
}

func TestBootstrapConfidenceRandomSeed(t *testing.T) {
	A := []float64{100, 101, 99, 98, 102, 103, 97, 104, 96, 105}
	B := []float64{120, 118, 122, 119, 121, 117, 123, 116, 124, 115}
	thresholds := []float64{0.1, 0.2, 0.3}
	reps := uint64(1_000_000)

	conf1 := BootstrapConfidence(A, B, thresholds, reps, 0)
	conf2 := BootstrapConfidence(A, B, thresholds, reps, 0)

	if reflect.DeepEqual(conf1, conf2) {
		t.Errorf("Expected different results with random seed, got identical")
	}
}

func TestBootstrapConfidenceRange(t *testing.T) {
	// Property: Für beliebige Eingaben liegt conf[t] ∈ [0, 1]
	prop := func(A, B []float64) bool {
		if len(A) == 0 || len(B) == 0 {
			return true // skip invalid input
		}

		thresholds := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9}
		reps := uint64(100)
		seed := rand.Uint64()&0xFFFFFFFFFFFFFFE + 1 // avoid zero seed

		conf := BootstrapConfidence(A, B, thresholds, reps, seed)

		for _, threshold := range thresholds {
			v := conf[threshold]
			if v < 0.0 || v > 1.0 || !isFinite(v) {
				t.Logf("Invalid confidence value: %.4f for threshold %.2f", v, threshold)
				return false
			}
		}
		return true
	}

	if err := quick.Check(prop, &quick.Config{
		MaxCount: 10_000,
		Rand:     rand.New(rand.NewSource(99)),
	}); err != nil {
		t.Error(err)
	}
}

func isFinite(f float64) bool {
	return !((f != f) || (f > 1e308) || (f < -1e308)) // exclude NaN, Inf
}

func TestBootstrapConfidenceMonotony(t *testing.T) {
	n := 1000
	x := 0.05     // let A be 5% faster
	sigma := 15.0 // noise standard deviation
	reps := uint64(10_000)
	seed := uint64(0)

	A := make([]float64, n)
	B := make([]float64, n)
	for i := range n {
		valA := 100.0 + rand.NormFloat64()*sigma
		valB := 100.0*(1+x) + rand.NormFloat64()*sigma
		A[i] = valA
		B[i] = valB
	}

	thresholds := []float64{x - 0.02, x - 0.01, x, x + 0.01, x + 0.02}
	conf := BootstrapConfidence(A, B, thresholds, reps, seed)

	// Prüfe, ob Konfidenz streng monoton fallend ist
	for i := 1; i < len(thresholds); i++ {
		if conf[thresholds[i]] > conf[thresholds[i-1]] {
			t.Errorf("Confidence not decreasing: conf[%.2f]=%.3f > conf[%.2f]=%.3f",
				thresholds[i], conf[thresholds[i]], thresholds[i-1], conf[thresholds[i-1]])
		}
	}
}

func TestBootstrapConfidence_RepsZero(t *testing.T) {
	a := []float64{1.0, 2.0, 3.0}
	b := []float64{1.0, 2.0, 3.0}
	thresholds := []float64{0.0, 0.1, 0.5}

	conf := BootstrapConfidence(a, b, thresholds, 0, 42)

	for _, th := range thresholds {
		v, ok := conf[th]
		if !ok {
			t.Fatalf("missing threshold %v in result map", th)
		}
		if !math.IsNaN(v) {
			t.Fatalf("expected NaN for threshold %v when reps==0, got %v", th, v)
		}
	}
}

func TestBootstrapConfidence_EdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		A, B       []float64
		thresholds []float64
		reps       uint64
		want       map[float64]float64
	}{
		{
			name:       "medA NaN",
			A:          []float64{math.NaN(), math.NaN(), math.NaN()},
			B:          []float64{1, 2, 3},
			thresholds: []float64{0.0},
			reps:       1,
			want:       map[float64]float64{0.0: 0.0},
		},
		{
			name:       "medB NaN",
			A:          []float64{1, 2, 3},
			B:          []float64{math.NaN(), math.NaN(), math.NaN()},
			thresholds: []float64{0.0},
			reps:       1,
			want:       map[float64]float64{0.0: 0.0},
		},
		{
			name:       "both zero medians",
			A:          []float64{0, 0, 0},
			B:          []float64{0, 0, 0},
			thresholds: []float64{0.0, 0.1},
			reps:       1,
			want:       map[float64]float64{0.0: 1.0, 0.1: 0.0},
		},
		{
			name:       "medA equals medB",
			A:          []float64{5, 5, 5},
			B:          []float64{5, 5, 5},
			thresholds: []float64{0.0, 0.1},
			reps:       1,
			want:       map[float64]float64{0.0: 1.0, 0.1: 0.0},
		},
		{
			name:       "both -Inf",
			A:          []float64{math.Inf(-1), math.Inf(-1)},
			B:          []float64{math.Inf(-1), math.Inf(-1)},
			thresholds: []float64{0.0},
			reps:       1,
			want:       map[float64]float64{0.0: 1.0},
		},
		{
			name:       "both +Inf",
			A:          []float64{math.Inf(1), math.Inf(1)},
			B:          []float64{math.Inf(1), math.Inf(1)},
			thresholds: []float64{0.0},
			reps:       1,
			want:       map[float64]float64{0.0: 1.0},
		},
		{
			name:       "medB zero, medA non-zero (eps branch)",
			A:          []float64{1.0},
			B:          []float64{0.0},
			thresholds: []float64{0.0},
			reps:       1,
			want:       map[float64]float64{0.0: 0.0},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			conf := BootstrapConfidence(tc.A, tc.B, tc.thresholds, tc.reps, 42)
			for _, th := range tc.thresholds {
				got, ok := conf[th]
				if !ok {
					t.Fatalf("missing threshold %v in result", th)
				}
				want := tc.want[th]
				if math.IsNaN(want) {
					if !math.IsNaN(got) {
						t.Fatalf("expected NaN for threshold %v, got %v", th, got)
					}
				} else {
					if got != want {
						t.Fatalf("unexpected confidence for %s threshold %v: got %v want %v", tc.name, th, got, want)
					}
				}
			}
		})
	}
}

// Tests for negative values in relativeGains. These use deterministic, identical
// samples so every bootstrap replicate produces the same medians and the
// resulting confidence is either 0.0 or 1.0 depending on the threshold.
func TestBootstrapConfidence_NegativeRelativeGains_DeterministicIdenticalSamples(t *testing.T) {
	A := []float64{103, 103, 103, 103, 103}
	B := []float64{100, 100, 100, 100, 100}

	thresholds := []float64{-0.05, 0.0, 0.01}
	// Use a small number of resamples; samples are identical so every replicate is the same.
	resamples := uint64(10)
	seed := uint64(42)

	conf := BootstrapConfidence(A, B, thresholds, resamples, seed)

	if got := conf[-0.05]; got != 1.0 {
		t.Fatalf("expected confidence 1.0 for threshold -0.05, got %v", got)
	}
	if got := conf[0.0]; got != 0.0 {
		t.Fatalf("expected confidence 0.0 for threshold 0.0, got %v", got)
	}
	if got := conf[0.01]; got != 0.0 {
		t.Fatalf("expected confidence 0.0 for threshold 0.01, got %v", got)
	}
}

func TestBootstrapConfidence_NegativeRelativeGains_ZeroDeltaCountsForNegative(t *testing.T) {
	// identical medians -> delta == 0.0
	A := []float64{100, 100, 100}
	B := []float64{100, 100, 100}

	thresholds := []float64{-0.01, 0.0}
	resamples := uint64(5)
	seed := uint64(7)

	conf := BootstrapConfidence(A, B, thresholds, resamples, seed)

	if got := conf[-0.01]; got != 1.0 {
		t.Fatalf("expected confidence 1.0 for threshold -0.01 when delta==0, got %v", got)
	}
	if got := conf[0.0]; got != 1.0 {
		t.Fatalf("expected confidence 1.0 for threshold 0.0 when delta==0, got %v", got)
	}
}

// Test for gains that are greater than 100% (i.e., medA > 2 * medB). These use
// deterministic, identical samples so every bootstrap replicate produces the same
// medians and the resulting confidence is either 0.0 or 1.0 depending on the threshold.
func TestBootstrapConfidence_HighRelativeGains_DeterministicIdenticalSamples(t *testing.T) {
	A := []float64{100, 100, 100, 100, 100}
	B := []float64{250, 250, 250, 250, 250}

	thresholds := []float64{0.5, 0.6, 0.66667} // 2x/50% faster, 2.5x/60% faster, 3x/66.67% faster
	// Use a small number of resamples; samples are identical so every replicate is the same.
	resamples := uint64(10)
	seed := uint64(42)

	conf := BootstrapConfidence(A, B, thresholds, resamples, seed)

	if got := conf[0.5]; got != 1.0 {
		t.Fatalf("expected confidence 1.0 for threshold 0.5, got %v", got)
	}
	if got := conf[0.6]; got != 1.0 {
		t.Fatalf("expected confidence 1.0 for threshold 0.6, got %v", got)
	}
	if got := conf[0.66667]; got != 0.0 {
		t.Fatalf("expected confidence 0.0 for threshold 0.66667, got %v", got)
	}

}

func TestF2T(t *testing.T) {
	tests := []struct {
		name        string
		timesFaster float64
		expected    float64
		expectNaN   bool
		description string
	}{
		{
			name:        "zero input",
			timesFaster: 0,
			expectNaN:   true,
			description: "timesFaster=0 should return NaN",
		},
		{
			name:        "negative input",
			timesFaster: -1.0,
			expectNaN:   true,
			description: "negative timesFaster should return NaN",
		},
		{
			name:        "NaN input",
			timesFaster: math.NaN(),
			expectNaN:   true,
			description: "NaN input should return NaN",
		},
		{
			name:        "positive infinity",
			timesFaster: math.Inf(1),
			expected:    1.0,
			expectNaN:   false,
			description: "+Inf should return 1.0 (1 - 1/+Inf = 1 - 0 = 1)",
		},
		{
			name:        "negative infinity",
			timesFaster: math.Inf(-1),
			expectNaN:   true,
			description: "-Inf should return NaN (negative value)",
		},
		{
			name:        "one times faster",
			timesFaster: 1.0,
			expected:    0.0,
			expectNaN:   false,
			description: "1x faster should return 0.0 (no speedup)",
		},
		{
			name:        "two times faster",
			timesFaster: 2.0,
			expected:    0.5,
			expectNaN:   false,
			description: "2x faster should return 0.5 (50% reduction)",
		},
		{
			name:        "three times faster",
			timesFaster: 3.0,
			expected:    1.0 - 1.0/3.0,
			expectNaN:   false,
			description: "3x faster should return ~0.6667",
		},
		{
			name:        "four times faster",
			timesFaster: 4.0,
			expected:    0.75,
			expectNaN:   false,
			description: "4x faster should return 0.75 (75% reduction)",
		},
		{
			name:        "ten times faster",
			timesFaster: 10.0,
			expected:    0.9,
			expectNaN:   false,
			description: "10x faster should return 0.9 (90% reduction)",
		},
		{
			name:        "very large value",
			timesFaster: 1e10,
			expected:    1.0 - 1.0/1e10,
			expectNaN:   false,
			description: "very large multiplier should approach 1.0",
		},
		{
			name:        "small positive value",
			timesFaster: 0.5,
			expected:    -1.0,
			expectNaN:   false,
			description: "0.5x faster (slower) should return -1.0 (negative threshold for slowdown)",
		},
		{
			name:        "just above zero",
			timesFaster: 1e-10,
			expected:    1.0 - 1.0/1e-10,
			expectNaN:   false,
			description: "very small positive value should return large negative number",
		},
		{
			name:        "1.5 times faster",
			timesFaster: 1.5,
			expected:    1.0 - 1.0/1.5,
			expectNaN:   false,
			description: "1.5x faster should return ~0.3333",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := F2T(tc.timesFaster)

			if tc.expectNaN {
				if !math.IsNaN(result) {
					t.Errorf("%s: expected NaN, got %v", tc.description, result)
				}
			} else {
				if math.IsNaN(result) {
					t.Errorf("%s: expected %v, got NaN", tc.description, tc.expected)
				} else if math.Abs(result-tc.expected) > 1e-10 {
					t.Errorf("%s: expected %v, got %v", tc.description, tc.expected, result)
				}
			}
		})
	}
}

func TestF2TEdgeCases(t *testing.T) {
	// Test that F2T is consistent with the formula: threshold = 1 - 1/timesFaster
	// For timesFaster > 1, the threshold should be positive and < 1 (speedup)
	// For timesFaster = 1, threshold should be 0 (no change)
	// For 0 < timesFaster < 1, the threshold is negative (slowdown)
	// For timesFaster <= 0 or NaN, returns NaN (invalid input)

	t.Run("boundary at 1", func(t *testing.T) {
		result := F2T(1.0)
		if result != 0.0 {
			t.Errorf("F2T(1.0) should be exactly 0.0, got %v", result)
		}
	})

	t.Run("just below 1", func(t *testing.T) {
		result := F2T(0.9999)
		expected := 1.0 - 1.0/0.9999
		if math.Abs(result-expected) > 1e-10 {
			t.Errorf("F2T(0.9999) should be %v (negative threshold for slowdown), got %v", expected, result)
		}
	})

	t.Run("just above 1", func(t *testing.T) {
		result := F2T(1.0001)
		expected := 1.0 - 1.0/1.0001
		if math.Abs(result-expected) > 1e-10 {
			t.Errorf("F2T(1.0001) should be %v, got %v", expected, result)
		}
	})

	t.Run("mathematical consistency", func(t *testing.T) {
		// Test that the formula is correct for various inputs
		testValues := []float64{1.1, 1.25, 1.5, 2.0, 3.0, 5.0, 100.0, 1000.0}
		for _, tf := range testValues {
			result := F2T(tf)
			expected := 1.0 - 1.0/tf
			if math.Abs(result-expected) > 1e-10 {
				t.Errorf("F2T(%v) = %v, expected %v", tf, result, expected)
			}
		}
	})
}

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

func TestBlockSampleClampsToHalfTheInput(t *testing.T) {
	// A block as long as the input leaves one start position, so every
	// replicate would be the input itself. The clamp has to prevent that, and
	// the way to see it is that replicates must differ from one another.
	xs := make([]float64, 40)
	for i := range xs {
		xs[i] = float64(i)
	}
	rng := NewDPRNG(31337)
	for _, L := range []int{20, 40, 100, -5, 0} {
		identical := 0
		const draws = 100
		for range draws {
			s := blockSample(xs, L, rng.UInt32N)
			if len(s) != len(xs) {
				t.Fatalf("L=%d: replicate has %d values, want %d", L, len(s), len(xs))
			}
			if slices.Equal(s, xs) {
				identical++
			}
		}
		// Even at the clamped maximum of n/2 there are 21 start positions, so
		// reproducing the input exactly is rare. Every replicate doing it means
		// the sampler degenerated.
		if identical > draws/2 {
			t.Errorf("L=%d: %d of %d replicates reproduced the input exactly; the sampler degenerated",
				L, identical, draws)
		}
	}
}

func TestBlockSampleSurvivesTinyInputs(t *testing.T) {
	// The clamp must not underflow the start count on inputs too short to hold
	// two blocks.
	rng := NewDPRNG(11)
	for n := 1; n <= 4; n++ {
		xs := make([]float64, n)
		for i := range xs {
			xs[i] = float64(i)
		}
		for _, L := range []int{-1, 0, 1, n, n + 5} {
			s := blockSample(xs, L, rng.UInt32N)
			if len(s) != n {
				t.Errorf("n=%d, L=%d: replicate has %d values, want %d", n, L, len(s), n)
			}
			for _, v := range s {
				if v < 0 || v >= float64(n) {
					t.Errorf("n=%d, L=%d: value %v is not one of the inputs", n, L, v)
				}
			}
		}
	}
}

func TestBlockBootstrapRejectsDegenerateLengths(t *testing.T) {
	// Identical distributions, so the confidence must land near 0.5. Before the
	// clamp, an oversized or negative block length produced exactly 0 or 1,
	// which reads as certainty.
	rng := NewDPRNG(12345)
	a := make([]float64, 101)
	b := make([]float64, 101)
	for i := range a {
		a[i] = 100 + rng.Float64()*10
		b[i] = 100 + rng.Float64()*10
	}
	for _, L := range []int{0, 1, 5, 50, 101, 200, -5} {
		c := BlockBootstrapConfidence(a, b, []float64{0.0}, 4000, L, 99)[0.0]
		if c <= 0.02 || c >= 0.98 {
			t.Errorf("blockLength %d: confidence %.4f on A/A data is degenerate, expected something near 0.5", L, c)
		}
	}
}

// sampleAB returns two well-separated samples: A is reliably faster than B.
func sampleAB() (a, b []float64) {
	a = make([]float64, 12)
	b = make([]float64, 12)
	for i := range a {
		a[i] = 10
		b[i] = 20
	}
	return a, b
}

func TestCompareSamplesDuplicateThresholdsKeepConfidenceInRange(t *testing.T) {
	a, b := sampleAB()
	res, err := CompareSamples(a, b, []float64{0.2, 0.2, 0.2}, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res) != 1 {
		t.Errorf("expected a single result for a threshold listed three times, got %d: %+v", len(res), res)
	}
	for _, r := range res {
		if r.Confidence < 0 || r.Confidence > 1 {
			t.Errorf("confidence %v for threshold %v is outside [0,1]", r.Confidence, r.RelativeSpeedupSampleAvsSampleB)
		}
	}
}

func TestCompareSamplesDuplicatesMatchDistinctInput(t *testing.T) {
	// Listing a threshold repeatedly must not change its confidence.
	a, b := sampleAB()
	withDupes, err := CompareSamples(a, b, []float64{0.1, 0.3, 0.3, 0.1, 0.3}, 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	distinct, err := CompareSamples(a, b, []float64{0.1, 0.3}, 2000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(withDupes) != len(distinct) {
		t.Fatalf("expected %d results, got %d", len(distinct), len(withDupes))
	}
	for i := range distinct {
		if withDupes[i] != distinct[i] {
			t.Errorf("result %d differs: with duplicates %+v, distinct %+v", i, withDupes[i], distinct[i])
		}
	}
}

func TestCompareSamplesDoesNotMutateCallerSlice(t *testing.T) {
	a, b := sampleAB()
	gains := []float64{0.5, 0.1, 0.3, 0.1}
	original := slices.Clone(gains)

	if _, err := CompareSamples(a, b, gains, 500); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(gains, original) {
		t.Errorf("CompareSamples modified the caller's slice: got %v, want %v", gains, original)
	}
}

func TestCompareSamplesResultsAreSortedAndDistinct(t *testing.T) {
	a, b := sampleAB()
	res, err := CompareSamples(a, b, []float64{0.5, 0.1, 0.3, 0.1, 0.5}, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []float64{0.1, 0.3, 0.5}
	if len(res) != len(want) {
		t.Fatalf("expected %d distinct thresholds, got %d: %+v", len(want), len(res), res)
	}
	for i, w := range want {
		if res[i].RelativeSpeedupSampleAvsSampleB != w {
			t.Errorf("result %d has threshold %v, want %v", i, res[i].RelativeSpeedupSampleAvsSampleB, w)
		}
	}
}

func TestBootstrapConfidenceDuplicateThresholds(t *testing.T) {
	a, b := sampleAB()
	conf := BootstrapConfidence(a, b, []float64{0.2, 0.2, 0.2}, 1000, 42)
	if len(conf) != 1 {
		t.Errorf("expected one map entry for one distinct threshold, got %d: %v", len(conf), conf)
	}
	v, ok := conf[0.2]
	if !ok {
		t.Fatalf("threshold 0.2 missing from result: %v", conf)
	}
	if v < 0 || v > 1 {
		t.Errorf("confidence %v is outside [0,1]", v)
	}
}

func TestBootstrapConfidenceDoesNotMutateCallerSlice(t *testing.T) {
	a, b := sampleAB()
	gains := []float64{0.4, 0.2, 0.4}
	original := slices.Clone(gains)

	BootstrapConfidence(a, b, gains, 200, 7)
	if !slices.Equal(gains, original) {
		t.Errorf("BootstrapConfidence modified the caller's slice: got %v, want %v", gains, original)
	}
}

func TestBootstrapConfidenceZeroResamplesWithDuplicates(t *testing.T) {
	a, b := sampleAB()
	conf := BootstrapConfidence(a, b, []float64{0.3, 0.3}, 0, 42)
	if len(conf) != 1 {
		t.Fatalf("expected one map entry, got %d: %v", len(conf), conf)
	}
	if v := conf[0.3]; !math.IsNaN(v) {
		t.Errorf("expected NaN for zero resamples, got %v", v)
	}
}

func TestBootstrapConfidenceEmptyGainsStaysEmpty(t *testing.T) {
	// BootstrapConfidence has no default threshold; that belongs to
	// CompareSamples. Empty input must keep yielding an empty map.
	a, b := sampleAB()
	if conf := BootstrapConfidence(a, b, nil, 100, 42); len(conf) != 0 {
		t.Errorf("expected an empty map for empty relativeGains, got %v", conf)
	}
}

func TestDedupeSortedCopy(t *testing.T) {
	cases := []struct {
		name string
		in   []float64
		want []float64
	}{
		{"empty", []float64{}, []float64{}},
		{"already distinct", []float64{0.1, 0.2}, []float64{0.1, 0.2}},
		{"unsorted with duplicates", []float64{0.3, 0.1, 0.3, 0.2, 0.1}, []float64{0.1, 0.2, 0.3}},
		{"all identical", []float64{0.5, 0.5, 0.5}, []float64{0.5}},
		{"negatives", []float64{0.1, -0.05, -0.05, 0}, []float64{-0.05, 0, 0.1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dedupeSortedCopy(c.in)
			if !slices.Equal(got, c.want) {
				t.Errorf("dedupeSortedCopy(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestUniqueSortedThresholdsDefaultsForEmpty(t *testing.T) {
	if got := uniqueSortedThresholds(nil); !slices.Equal(got, []float64{0.0}) {
		t.Errorf("uniqueSortedThresholds(nil) = %v, want [0]", got)
	}
	if got := uniqueSortedThresholds([]float64{}); !slices.Equal(got, []float64{0.0}) {
		t.Errorf("uniqueSortedThresholds(empty) = %v, want [0]", got)
	}
}

func TestConfidenceStaysInRangeForManyDuplicates(t *testing.T) {
	// The original defect scaled with the number of repetitions: 20 copies of a
	// threshold produced a confidence of 20.
	a, b := sampleAB()
	gains := make([]float64, 20)
	for i := range gains {
		gains[i] = 0.25
	}
	res, err := CompareSamples(a, b, gains, 500)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res) != 1 {
		t.Fatalf("expected one result, got %d", len(res))
	}
	if res[0].Confidence < 0 || res[0].Confidence > 1 {
		t.Errorf("confidence %v is outside [0,1]", res[0].Confidence)
	}
}

// TestBootstrapConfidenceCryptoPathReusesOneGenerator guards the fix for the
// unseeded sampling path.
//
// Building a CPRNG per bootstrap sample allocated an 8 KiB buffer and filled it
// completely from crypto/rand, twice per replicate, to consume a few hundred
// bytes of it. At the size used here that came to roughly 16 MiB of buffers per
// call. Reusing a single generator leaves only the sample slices.
func TestBootstrapConfidenceCryptoPathReusesOneGenerator(t *testing.T) {
	a, b := sampleAB()
	const resamples = 1000
	gains := []float64{0.0}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	BootstrapConfidence(a, b, gains, resamples, 0)
	runtime.ReadMemStats(&after)
	allocated := after.TotalAlloc - before.TotalAlloc

	// The sample slices alone are 2*resamples*len*8 bytes, well under 1 MiB
	// here. The old behaviour allocated over an order of magnitude more.
	const budget = 2 << 20
	if allocated > budget {
		t.Errorf("BootstrapConfidence allocated %d bytes for %d resamples, above the %d byte budget; "+
			"the cryptographic generator is likely being rebuilt per sample again", allocated, resamples, budget)
	}
}

// TestBootstrapSampleCryptoSharesGeneratorStream checks that consecutive samples
// drawn from one generator are independent draws rather than a repeated
// sequence, which is what sharing a stream across samples relies on.
func TestBootstrapSampleCryptoSharesGeneratorStream(t *testing.T) {
	xs := make([]float64, 64)
	for i := range xs {
		xs[i] = float64(i)
	}
	rng := NewCPRNG(bootstrapCPRNGBufferBytes)

	first := bootstrapSampleCrypto(xs, rng)
	identical := 0
	for range 32 {
		next := bootstrapSampleCrypto(xs, rng)
		if slices.Equal(first, next) {
			identical++
		}
	}
	if identical > 0 {
		t.Errorf("%d of 32 consecutive samples repeated the first one; the generator stream is not advancing", identical)
	}
}

// TestBootstrapSampleSeededStaysReproducible pins the behaviour the seeded path
// exists for: the same seed must keep producing the same sample, so refactoring
// the unseeded path must not have disturbed it.
func TestBootstrapSampleSeededStaysReproducible(t *testing.T) {
	xs := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	first := bootstrapSampleSeeded(xs, 12345)
	second := bootstrapSampleSeeded(xs, 12345)
	if !slices.Equal(first, second) {
		t.Errorf("same seed produced different samples:\n%v\n%v", first, second)
	}
	if other := bootstrapSampleSeeded(xs, 12346); slices.Equal(first, other) {
		t.Error("different seeds produced identical samples")
	}
	// The delegating entry point must agree with the specialised one.
	if via := bootstrapSample(xs, 12345); !slices.Equal(first, via) {
		t.Errorf("bootstrapSample and bootstrapSampleSeeded disagree:\n%v\n%v", via, first)
	}
}

// --- F2: NaN thresholds ---

func TestCompareSamplesRejectsNaNThreshold(t *testing.T) {
	a, b := sampleAB()
	_, err := CompareSamples(a, b, []float64{0.1, math.NaN(), 0.3}, 500)
	if err == nil {
		t.Fatal("expected an error for a NaN threshold, got nil")
	}
	// The index must refer to the caller's slice, not to an internally sorted
	// copy, which would report 0 here because NaN sorts to the front.
	if !strings.Contains(err.Error(), "relativeGains[1]") {
		t.Errorf("error should name the caller's index 1, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "F2T") {
		t.Errorf("error should point at F2T as the likely source, got %q", err.Error())
	}
}

func TestCompareSamplesRejectsUncheckedF2TResult(t *testing.T) {
	// The concrete trap this guard exists for: F2T signals invalid input with
	// NaN, and that used to flow through to a plausible-looking {NaN, 0} result.
	a, b := sampleAB()
	res, err := CompareSamples(a, b, []float64{F2T(2.0), F2T(0)}, 500)
	if err == nil {
		t.Fatalf("expected an error for an unchecked F2T(0), got results %+v", res)
	}
	if len(res) != 0 {
		t.Errorf("expected no results alongside the error, got %+v", res)
	}
}

func TestCompareSamplesAcceptsInfiniteThresholds(t *testing.T) {
	// Infinities are degenerate but consistent, and are deliberately allowed.
	a, b := sampleAB() // A is reliably faster than B
	res, err := CompareSamples(a, b, []float64{math.Inf(-1), math.Inf(1)}, 1000)
	if err != nil {
		t.Fatalf("infinite thresholds must be accepted, got error: %v", err)
	}
	if len(res) != 2 {
		t.Fatalf("expected 2 results, got %d: %+v", len(res), res)
	}
	if got := res[0].RelativeSpeedupSampleAvsSampleB; !math.IsInf(got, -1) {
		t.Errorf("expected -Inf first after sorting, got %v", got)
	}
	if res[0].Confidence != 1.0 {
		t.Errorf("delta >= -Inf holds always, expected confidence 1, got %v", res[0].Confidence)
	}
	if res[1].Confidence != 0.0 {
		t.Errorf("no finite delta reaches +Inf, expected confidence 0, got %v", res[1].Confidence)
	}
}

func TestBootstrapConfidenceSkipsNaNThresholds(t *testing.T) {
	a, b := sampleAB()
	conf := BootstrapConfidence(a, b, []float64{0.1, math.NaN(), 0.3}, 500, 42)

	if len(conf) != 2 {
		t.Errorf("expected the NaN threshold to be dropped, leaving 2 entries, got %d: %v", len(conf), conf)
	}
	for th := range conf {
		if math.IsNaN(th) {
			t.Error("result map contains a NaN key, which no caller could ever look up")
		}
	}
	// The surviving thresholds must be unaffected.
	for _, th := range []float64{0.1, 0.3} {
		v, ok := conf[th]
		if !ok {
			t.Errorf("threshold %v missing from result: %v", th, conf)
			continue
		}
		if v < 0 || v > 1 {
			t.Errorf("confidence %v for threshold %v is outside [0,1]", v, th)
		}
	}
}

func TestBootstrapConfidenceOnlyNaNThresholdsYieldsEmptyMap(t *testing.T) {
	a, b := sampleAB()
	if conf := BootstrapConfidence(a, b, []float64{math.NaN(), math.NaN()}, 500, 42); len(conf) != 0 {
		t.Errorf("expected an empty map when every threshold is NaN, got %v", conf)
	}
}

func TestBootstrapConfidenceKeepsInfiniteThresholds(t *testing.T) {
	a, b := sampleAB()
	conf := BootstrapConfidence(a, b, []float64{math.Inf(-1), math.Inf(1)}, 500, 42)
	if len(conf) != 2 {
		t.Fatalf("expected both infinities to survive, got %d entries: %v", len(conf), conf)
	}
	if v, ok := conf[math.Inf(-1)]; !ok || v != 1.0 {
		t.Errorf("expected -Inf -> 1.0 and retrievable, got v=%v ok=%v", v, ok)
	}
	if v, ok := conf[math.Inf(1)]; !ok || v != 0.0 {
		t.Errorf("expected +Inf -> 0.0 and retrievable, got v=%v ok=%v", v, ok)
	}
}

func TestBootstrapConfidenceNaNZeroResamples(t *testing.T) {
	// The zero-resamples shortcut must drop NaN too, not emit an unreachable key.
	a, b := sampleAB()
	conf := BootstrapConfidence(a, b, []float64{0.2, math.NaN()}, 0, 42)
	if len(conf) != 1 {
		t.Fatalf("expected one entry, got %d: %v", len(conf), conf)
	}
	if v := conf[0.2]; !math.IsNaN(v) {
		t.Errorf("expected NaN confidence for zero resamples, got %v", v)
	}
}

// --- F3: one continuous stream per run ---

// serialCorrelation returns the lag-1 autocorrelation of xs.
func serialCorrelation(xs []float64) float64 {
	var mean float64
	for _, v := range xs {
		mean += v
	}
	mean /= float64(len(xs))
	var num, den float64
	for i := range len(xs) - 1 {
		num += (xs[i] - mean) * (xs[i+1] - mean)
	}
	for _, v := range xs {
		den += (v - mean) * (v - mean)
	}
	return num / den
}

// TestBootstrapSampleDPRNGHasNoSerialCorrelation guards against reintroducing a
// per-replicate seeding scheme.
//
// Seeding a fresh DPRNG per replicate from consecutive seeds used to leave a
// lag-1 correlation of 0.095 between the first index of consecutive samples,
// against a noise band of roughly 0.007 at the sample count used in that
// measurement. Drawing every replicate from one stream removes it.
func TestBootstrapSampleDPRNGHasNoSerialCorrelation(t *testing.T) {
	const (
		n          = 64
		replicates = 20_000
	)
	xs := make([]float64, n)
	for i := range xs {
		xs[i] = float64(i)
	}

	rng := NewDPRNG(0xDEADBEEF)
	first := make([]float64, replicates)
	for i := range replicates {
		first[i] = bootstrapSampleDPRNG(xs, &rng)[0]
	}

	got := serialCorrelation(first)
	// 3/sqrt(20000) is about 0.021; 0.05 leaves headroom for chance while still
	// rejecting the 0.095 the old scheme produced.
	const tolerance = 0.05
	if math.Abs(got) > tolerance {
		t.Errorf("lag-1 correlation between consecutive samples is %.4f, above the %.2f tolerance; "+
			"the generator is likely being reseeded per replicate again", got, tolerance)
	}
}

// TestBootstrapConfidenceSeededUsesOneStream checks that consecutive replicates
// of a seeded run differ, which a per-replicate reseeding scheme with a constant
// seed offset would not guarantee.
func TestBootstrapConfidenceSeededAdvancesTheStream(t *testing.T) {
	xs := make([]float64, 32)
	for i := range xs {
		xs[i] = float64(i)
	}
	rng := NewDPRNG(4242)
	prev := bootstrapSampleDPRNG(xs, &rng)
	repeats := 0
	for range 500 {
		next := bootstrapSampleDPRNG(xs, &rng)
		if slices.Equal(prev, next) {
			repeats++
		}
		prev = next
	}
	if repeats > 0 {
		t.Errorf("%d of 500 consecutive samples were identical; the stream is not advancing", repeats)
	}
}

// TestBootstrapConfidenceSeededStillDeterministic pins the property the seeded
// path exists for, which the switch to a shared stream must not disturb.
func TestBootstrapConfidenceSeededStillDeterministic(t *testing.T) {
	// Spread-out data, so that which values a replicate happens to draw actually
	// moves the median. With constant inputs every replicate yields the same
	// delta and the seed provably cannot matter.
	rng := NewDPRNG(7)
	a := make([]float64, 41)
	b := make([]float64, 41)
	for i := range a {
		a[i] = 100 + rng.Float64()*40
		b[i] = 118 + rng.Float64()*40
	}
	gains := []float64{0.1, 0.2}
	first := BootstrapConfidence(a, b, gains, 500, 99)
	second := BootstrapConfidence(a, b, gains, 500, 99)
	for _, g := range gains {
		if first[g] != second[g] {
			t.Errorf("same seed gave different confidences for threshold %v: %v vs %v", g, first[g], second[g])
		}
	}
	if other := BootstrapConfidence(a, b, gains, 500, 100); other[0.1] == first[0.1] && other[0.2] == first[0.2] {
		t.Error("different seeds produced identical results across both thresholds")
	}
}
