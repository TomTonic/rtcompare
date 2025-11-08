package rtcompare

import (
	"fmt"
	"math"
	"slices"
)

type RTcomparisonResult struct {
	RelativeSpeedupSampleAvsSampleB float64
	Confidence                      float64
}

const MinimumDataPoints uint64 = 11

// CompareRuntimes compares two samples of runtimes (in float64, e.g., milliseconds)
// and computes the confidence that sample A is faster than sample B by at least
// the specified relative speedups. The precisionLevel parameter controls the number of bootstrap
// repetitions (higher values yield more precise results of the statistical tests but take longer to compute).
// It returns a slice of RTcomparisonResult, each containing one of the given relative speedup and
// the corresponding confidence level calculated by the statistical test.
// If there are not enough data points in either sample, an error is returned.
func CompareRuntimes(sampleA, sampleB []float64, relativeSpeedupsToTest []float64, precisionLevel uint64) (result []RTcomparisonResult, err error) {
	if uint64(len(sampleA)) < MinimumDataPoints || uint64(len(sampleB)) < MinimumDataPoints {
		return []RTcomparisonResult{}, fmt.Errorf("not enough data points: need at least 11 runtimes for each of A and B")
	}
	if len(relativeSpeedupsToTest) == 0 {
		relativeSpeedupsToTest = []float64{0.0}
	}

	slices.Sort(relativeSpeedupsToTest)

	conf := BootstrapConfidence(sampleA, sampleB, relativeSpeedupsToTest, precisionLevel, 0)

	for _, t := range relativeSpeedupsToTest {
		r := RTcomparisonResult{
			RelativeSpeedupSampleAvsSampleB: t,
			Confidence:                      conf[t],
		}
		result = append(result, r)
	}
	return result, nil
}

// bootstrapSample returns a bootstrap sample (sampling with replacement) drawn from xs.
// The returned slice has the same length as xs and is populated by selecting random
// indices into xs using a deterministic PRNG initialized with prngSeed via NewDPRNG.
// The input slice is not modified.
//
// Each element of the result is chosen as xs[rng.Uint64()%uint64(len(xs))]. Callers should
// be aware that this uses a modulo reduction which can introduce slight bias when
// len(xs) does not evenly divide the PRNG range. Also ensure xs is non-empty when
// expecting sampled values, since index selection with len(xs)==0 would be invalid.
//
// This implementation uses a DPRNG from this package for reproducible sampling. Use 0 (zero)
// as prngSeed for a random seed value, or provide a specific non-zero seed for
// reproducible results across multiple calls.
func bootstrapSample(xs []float64, prngSeed uint64) []float64 {
	rng := NewDPRNG(prngSeed)
	n := len(xs)
	sample := make([]float64, n)
	if n == 0 {
		return sample
	}
	for i := range n {
		// sample[i] = xs[rng.Uint64()%uint64(n)]
		sample[i] = xs[rng.UInt32N(uint32(n))]
	}
	return sample
}

// BootstrapConfidence estimates the probability (confidence) that the relative speedup of A over B
// meets or exceeds each requested threshold using bootstrap resampling.
//
// The function performs `reps` bootstrap replicates. In each replicate it draws a bootstrap sample
// from A and from B (via bootstrapSample), computes their medians and evaluates the relative speedup as:
//
//	delta = 1 - median(A_sample)/median(B_sample)
//
// A positive delta indicates A is faster than B by that relative amount. For every threshold t in
// `thresholds` the function increments a counter when delta >= t. After all replicates it returns a map
// that maps each threshold to the estimated confidence (fraction of replicates meeting delta >= t).
//
// Numerical and edge-case behavior (important):
//   - If `reps` is zero the function returns a map with each threshold mapped to math.NaN().
//   - If either sample median is NaN (for example QuickMedian returned NaN for an empty sample), the
//     replicate produces delta = NaN and that replicate does not count as meeting any threshold.
//   - To avoid divide-by-zero and extreme ratios when median(B_sample) == 0 (or is numerically
//     extremely small), the implementation uses a small, scale-aware epsilon fallback. Concretely it
//     chooses an epsilon = max(|median(B)| * rel, SmallestNonzeroFloat64) with a small relative factor
//     (e.g. rel = 1e-12). If |median(B)| < epsilon the code uses epsilon as the denominator. This
//     guarantees a finite, bounded delta while preserving the correct ratio for typical non-zero medians.
//   - If both medians are zero (or both are equal/infinite in the same direction), the replicate sets
//     delta = 0.0 (no relative difference).
//
// Parameters:
//   - A, B: observed samples (e.g. runtimes or throughputs) used as the population for bootstrap sampling.
//   - thresholds: slice of relative-speedup thresholds to evaluate (e.g. 0.05 for 5% faster).
//   - reps: number of bootstrap replicates to run (the greater the reps, the lower the sampling error).
//   - prngSeed: DPRNG seed used for reproducible sampling. Use 0 to allow the function to initialize a
//     non-deterministic seed, or provide a specific non-zero seed to reproduce results across runs.
//
// Returns:
//
//	A map[float64]float64 where each key is a threshold from `thresholds` and the corresponding value is
//	the estimated confidence in [0,1] that the relative speedup of A over B is at least that threshold.
func BootstrapConfidence(A, B []float64, thresholds []float64, reps uint64, prngSeed uint64) (confidenceForThreshold map[float64]float64) {

	confidenceForThreshold = make(map[float64]float64, len(thresholds))

	if reps == 0 {
		for _, threshold := range thresholds {
			confidenceForThreshold[threshold] = math.NaN()
		}
		return confidenceForThreshold
	}

	counts := make(map[float64]uint32, len(thresholds))

	for range reps {
		sampleA := bootstrapSample(A, prngSeed)
		sampleB := bootstrapSample(B, prngSeed)
		medA := QuickMedian(sampleA)
		medB := QuickMedian(sampleB)

		var delta float64

		// robust: guard NaN and avoid divide-by-zero / huge ratios for tiny medB
		if math.IsNaN(medA) || math.IsNaN(medB) {
			delta = math.NaN()
		} else if (medA == 0 && medB == 0) || medA == medB || (math.IsInf(medA, -1) && math.IsInf(medB, -1)) || (math.IsInf(medA, 1) && math.IsInf(medB, 1)) {
			delta = 0.0
		} else {
			// relative epsilon scaled to medB to avoid large distortion
			rel := 1e-12
			eps := math.Max(math.Abs(medB)*rel, math.SmallestNonzeroFloat64)
			denom := medB
			if math.Abs(medB) < eps {
				// treat as effectively zero -> use eps as denominator
				denom = eps
			}
			delta = 1.0 - medA/denom
		}

		for _, threshold := range thresholds {
			if delta >= threshold {
				counts[threshold]++
			}
		}
	}

	for _, threshold := range thresholds {
		confidenceForThreshold[threshold] = float64(counts[threshold]) / float64(reps)
	}
	return confidenceForThreshold
}
