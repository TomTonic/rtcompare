package rtcompare

import (
	"fmt"
	"math"
	"slices"
)

// RTcomparisonResult holds the result of comparing two sets of runtime measurements.
// For each requested relative speedup threshold it contains the estimated confidence
// that the speedup of sample A over sample B meets or exceeds that threshold.
type RTcomparisonResult struct {
	// RelativeSpeedupSampleAvsSampleB is the relative speedup threshold that was evaluated.
	RelativeSpeedupSampleAvsSampleB float64
	// Confidence is the estimated confidence (in [0,1]) that the relative speedup of sample A over sample B
	// meets or exceeds RelativeSpeedupSampleAvsSampleB.
	Confidence float64
}

const MinimumDataPoints uint64 = 11

// DefaultResamples is a sensible package-level default for bootstrap resamples.
// Use this when you want a balanced trade-off between Monte-Carlo precision and
// runtime cost. This default (5k) follows common recommendations in the
// bootstrap literature; increase it for extreme-tail accuracy or highly precise
// confidence estimates.
const DefaultResamples uint64 = 5_000

// CompareSamples compares two sets of scalar measurements (for example: runtimes,
// memory footprints, or other numeric metrics) and estimates the confidence that
// values from `measurementsA` are smaller than those from `measurementsB` by at
// least the requested relative thresholds.
//
// The function is intentionally metric-agnostic: it treats each input slice as a
// sample of independent measurements where *smaller* values indicate a better
// outcome (this matches runtimes or memory consumption). If you have a
// "larger-is-better" metric (e.g., throughput), transform the inputs before
// calling this function (for example by taking the reciprocal or negating the
// values) so that smaller means better.
//
// For each bootstrap replicate the implementation draws a resampled population
// from `measurementsA` and `measurementsB`, computes their medians and evaluates
// the relative improvement as:
//
//	delta = 1 - median(A_sample)/median(B_sample)
//
// A positive `delta` indicates that `measurementsA` are smaller than
// `measurementsB` by that relative fraction (e.g. delta=0.2 → A is 20% smaller).
// For each requested relative gain the function reports the fraction of replicates
// where `delta >= threshold` as the confidence.
//
// Parameters:
//
//   - measurementsA, measurementsB: samples of scalar measurements (float64). Prefer
//     measurements that share the same units and scale (e.g., both in milliseconds).
//
//   - relativeGains: relative improvement thresholds to evaluate (e.g. 0.05 means
//     "A is at least 5% smaller than B"). If nil or empty, the function evaluates
//     a single relative gain at 0.0 (is A smaller than B at all?).
//
//     Negative values in `relativeGains` are allowed and are interpreted as
//     tolerated relative *slowdowns* of A vs. B. Concretely, a threshold `t < 0`
//     is evaluated as `delta >= t` where `delta = 1 - median(A)/median(B)`. For
//     example, `t = -0.05` corresponds to the statement "A is not more than 5% slower
//     than B" (i.e., A is within 5% of B). A replicate with `delta = -0.03` would
//     count as meeting `t = -0.05` because `-0.03 >= -0.05`.
//
//     Use negative thresholds when you want to ask whether A is *within* a relative
//     tolerance of B rather than strictly faster. Zero remains the boundary
//     "is A smaller than B?" and positive thresholds require A to be faster by at
//     least that relative fraction.
//
//   - resamples: number of bootstrap resamples to run (larger → more precise estimates,
//     longer runtime). See the note in `BootstrapConfidence` for guidance and literature
//     references about choosing the number of resamples.
//
// Returns a slice of RTcomparisonResult where each entry contains the requested
// relative threshold and the corresponding confidence in [0,1]. If either input
// contains fewer than `MinimumDataPoints` values an error is returned.
func CompareSamples(measurementsA, measurementsB []float64, relativeGains []float64, resamples uint64) (result []RTcomparisonResult, err error) {
	if uint64(len(measurementsA)) < MinimumDataPoints || uint64(len(measurementsB)) < MinimumDataPoints {
		return []RTcomparisonResult{}, fmt.Errorf("not enough data points: need at least %d measurements for each input", MinimumDataPoints)
	}
	if len(relativeGains) == 0 {
		relativeGains = []float64{0.0}
	}

	slices.Sort(relativeGains)

	conf := BootstrapConfidence(measurementsA, measurementsB, relativeGains, resamples, 0)

	for _, t := range relativeGains {
		r := RTcomparisonResult{
			RelativeSpeedupSampleAvsSampleB: t,
			Confidence:                      conf[t],
		}
		result = append(result, r)
	}
	return result, nil
}

// CompareRuntimesDefault calls CompareRuntimes using `DefaultResamples`.
// This convenience wrapper avoids repeating the numeric literal in callers
// and documents the recommended default in the public API.
func CompareSamplesDefault(measurementsA, measurementsB []float64, relativeGains []float64) (result []RTcomparisonResult, err error) {
	return CompareSamples(measurementsA, measurementsB, relativeGains, DefaultResamples)
}

// Deprecated: Use CompareSamples instead. This function is retained for backward compatibility.
func CompareRuntimes(measurementsA, measurementsB []float64, relativeGains []float64, resamples uint64) (result []RTcomparisonResult, err error) {
	return CompareSamples(measurementsA, measurementsB, relativeGains, resamples)
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
// This implementation uses a DPRNG from this package for reproducible sampling.
// Provide a specific non-zero seed for reproducible results across multiple calls.
// If prngSeed is zero, the function uses a CPRNG with cryptographic strength randomness.
func bootstrapSample(xs []float64, prngSeed uint64) []float64 {
	n := len(xs)
	sample := make([]float64, n)
	if n == 0 {
		return sample
	}
	if prngSeed != 0 {
		rng := NewDPRNG(prngSeed)
		for i := range n {
			// sample[i] = xs[rng.Uint64()%uint64(n)]
			sample[i] = xs[rng.UInt32N(uint32(n))]
		}
	} else {
		rng := NewCPRNG(8192)
		for i := range n {
			sample[i] = xs[rng.Uint32N(uint32(n))]
		}
	}
	return sample
}

// BootstrapConfidence estimates the probability (confidence) that the relative speedup of A over B
// meets or exceeds each requested threshold using bootstrap resampling.
//
// The function performs `resamples` bootstrap replicates. In each replicate it draws a bootstrap sample
// from A and from B (via bootstrapSample), computes their medians and evaluates the relative speedup as:
//
//	delta = 1 - median(A_sample)/median(B_sample)
//
// A positive delta indicates A is faster than B by that relative amount. For every threshold t in
// `relativeGains` the function increments a counter when delta >= t. After all replicates it returns a map
// that maps each threshold to the estimated confidence (fraction of replicates meeting delta >= t).
//
// Numerical and edge-case behavior (important):
//   - If `resamples` is zero the function returns a map with each threshold mapped to math.NaN().
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
//   - relativeGains: slice of relative-speedup thresholds to evaluate (e.g. 0.05 for 5% faster).
//   - resamples: number of bootstrap resamples to run (the greater the resamples, the lower the Monte Carlo sampling error).
//   - prngSeed: DPRNG seed used for reproducible sampling. Provide a specific non-zero seed to reproduce results across runs.
//     If prngSeed is zero, the function uses a CPRNG with cryptographic strength randomness.
//
// Note on choosing `resamples` (literature guidance): There is no one-size-fits-all value; common
// recommendations in the bootstrap literature (Efron & Tibshirani; Davison & Hinkley) are to use at
// least 1,000 resamples for standard-error estimation and often 5,000–10,000 (or more) when estimating
// percentile confidence intervals, especially for tail probabilities. The Monte Carlo error of a
// proportion estimated from resamples decreases approximately as 1/sqrt(R) where R is the number of
// resamples, so doubling `resamples` reduces that error by about 1/sqrt(2). For many practical uses
// `resamples` in the range 1,000–10,000 is a reasonable default; increase it when you need precise
// confidence estimates near extreme thresholds or when you require reproducible low-variance results.
//
// Returns:
//
//	A map[float64]float64 where each key is a threshold from `thresholds` and the corresponding value is
//	the estimated confidence in [0,1] that the relative speedup of A over B is at least that threshold.
func BootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, prngSeed uint64) (confidenceForThreshold map[float64]float64) {

	confidenceForThreshold = make(map[float64]float64, len(relativeGains))

	if resamples == 0 {
		for _, threshold := range relativeGains {
			confidenceForThreshold[threshold] = math.NaN()
		}
		return confidenceForThreshold
	}

	counts := make(map[float64]uint32, len(relativeGains))

	for i := uint64(0); i < resamples; i++ {
		var seedA, seedB uint64
		if prngSeed == 0 {
			// Preserve any default/non-deterministic behavior of bootstrapSample when seed is zero.
			seedA = 0
			seedB = 0
		} else {
			// Derive iteration-specific, distinct seeds for A and B from the base seed.
			iterSeed := prngSeed + i
			seedA = iterSeed*2 + 1
			seedB = iterSeed*2 + 2
		}

		sampleA := bootstrapSample(A, seedA)
		sampleB := bootstrapSample(B, seedB)
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

		for _, threshold := range relativeGains {
			if delta >= threshold {
				counts[threshold]++
			}
		}
	}

	for _, threshold := range relativeGains {
		confidenceForThreshold[threshold] = float64(counts[threshold]) / float64(resamples)
	}
	return confidenceForThreshold
}

// F2T (FactorToThreshold) converts a multiplicative speedup timesFaster (e.g. 3.0 => A is 3× faster)
// to the internal relative‑reduction threshold used by CompareSamples and BootstrapConfidence.
func F2T(timesFaster float64) float64 {
	if timesFaster <= 0 || math.IsNaN(timesFaster) {
		return math.NaN()
	}
	return 1.0 - 1.0/timesFaster
}
