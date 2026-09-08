package rtcompare

import (
	"math"
	"runtime"
	"slices"
	"strings"
	"testing"
)

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
