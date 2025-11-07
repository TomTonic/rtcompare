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
	_, err := CompareRuntimes(A, B, []float64{0.1}, 1000)
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
	results, err := CompareRuntimes(A, B, nil, 1000)
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
	results, err := CompareRuntimes(A, B, thresholds, 1000)
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
	results, err := CompareRuntimes(A, B, thresholds, 1000)
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

	conf1 := bootstrapConfidence(A, B, thresholds, reps, seed)
	conf2 := bootstrapConfidence(A, B, thresholds, reps, seed)

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

	conf := bootstrapConfidence(A, B, thresholds, reps, seed)

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

	conf := bootstrapConfidence(A, B, thresholds, reps, seed)

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

	conf := bootstrapConfidence(A, B, thresholds, reps, seed)
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

	conf1 := bootstrapConfidence(A, B, thresholds, reps, 0)
	conf2 := bootstrapConfidence(A, B, thresholds, reps, 0)

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

		conf := bootstrapConfidence(A, B, thresholds, reps, seed)

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
	conf := bootstrapConfidence(A, B, thresholds, reps, seed)

	// Prüfe, ob Konfidenz streng monoton fallend ist
	for i := 1; i < len(thresholds); i++ {
		if conf[thresholds[i]] > conf[thresholds[i-1]] {
			t.Errorf("Confidence not decreasing: conf[%.2f]=%.3f > conf[%.2f]=%.3f",
				thresholds[i], conf[thresholds[i]], thresholds[i-1], conf[thresholds[i-1]])
		}
	}
}
