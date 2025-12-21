package rtcompare

import (
	"math"
	"math/rand"
	"reflect"
	"slices"
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
		name         string
		timesFaster  float64
		expected     float64
		expectNaN    bool
		description  string
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
	// For timesFaster > 1, the threshold should be positive and < 1
	// For timesFaster = 1, threshold should be 0
	// For timesFaster < 1 (but > 0), this would be a slowdown, should return NaN
	
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
