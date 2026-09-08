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
//     a single relative gain at 0.0.
//
//     A threshold of 0.0 deserves a word of warning, because it is the one place
//     where the inclusive comparison bites. Every threshold is evaluated as
//     `delta >= t`, so at t = 0 the question is "is A at least as small as B",
//     not "is A smaller". Replicates in which both medians come out exactly equal
//     count towards the confidence.
//
//     With quantized inputs such as timings, that is not a rare corner case. A
//     measurement is an integer count of clock ticks divided by a batch size, so
//     distinct measurements collapse onto identical values: at this package's
//     default calibration target, an ordinary run tied in about 15% of
//     replicates, lifting the confidence at t = 0 by half that. Ask for a
//     threshold above zero if you mean strictly faster, or shrink the
//     quantization; see CollectOptions.MaxQuantizationError and the tie rate
//     reported by ValidateHarness.
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
//
// The results are ordered by ascending threshold and contain one entry per
// *distinct* threshold: passing the same value several times yields a single
// result for it, not several. The caller's `relativeGains` slice is left
// untouched; sorting and deduplication happen on an internal copy.
//
// Non-finite thresholds are treated differently from one another:
//
//   - NaN is rejected with an error naming its index. It cannot be answered
//     meaningfully, since `delta >= NaN` is false for every delta, and the
//     resulting confidence of zero would be indistinguishable from a genuine
//     result. Watch for this when feeding [F2T] results in unchecked: F2T
//     signals invalid input by returning NaN.
//   - +Inf and -Inf are accepted. They are degenerate but well defined and
//     internally consistent: `delta >= -Inf` holds for every non-NaN delta, so
//     -Inf always yields confidence 1, and no finite delta reaches +Inf, so
//     +Inf always yields confidence 0. They are of little practical use as
//     thresholds, but they do not misreport anything.
func CompareSamples(measurementsA, measurementsB []float64, relativeGains []float64, resamples uint64) (result []RTcomparisonResult, err error) {
	if uint64(len(measurementsA)) < MinimumDataPoints || uint64(len(measurementsB)) < MinimumDataPoints {
		return []RTcomparisonResult{}, fmt.Errorf("not enough data points: need at least %d measurements for each input", MinimumDataPoints)
	}
	// Reject NaN thresholds here, at the boundary, while the caller's own index
	// is still available to name in the error. A NaN cannot be reported on: it
	// is never >= anything, so it would score a confidence of zero, and zero is
	// a perfectly ordinary answer that the caller could not tell apart from a
	// real one.
	for i, g := range relativeGains {
		if math.IsNaN(g) {
			return []RTcomparisonResult{}, fmt.Errorf(
				"relativeGains[%d] is NaN, which is not a usable threshold; note that F2T returns NaN for factors <= 0 or NaN, so check its result before passing it on", i)
		}
	}
	thresholds := uniqueSortedThresholds(relativeGains)

	conf := BootstrapConfidence(measurementsA, measurementsB, thresholds, resamples, 0)

	for _, t := range thresholds {
		r := RTcomparisonResult{
			RelativeSpeedupSampleAvsSampleB: t,
			Confidence:                      conf[t],
		}
		result = append(result, r)
	}
	return result, nil
}

// dedupeSortedCopy returns the distinct values of gains in ascending order.
//
// It works on a copy, so the caller's slice is neither reordered nor shortened.
// That matters because these slices are usually literals reused across several
// comparisons, and silently sorting a caller's argument is a surprising side
// effect.
//
// Deduplication is what keeps confidences inside [0,1]: the bootstrap counts one
// hit per listed threshold per replicate, so a threshold appearing three times
// would be counted three times per replicate and yield a "confidence" of 3.
//
// Note that NaN values survive this function, and duplicates of them are not
// collapsed: slices.Compact compares with ==, and NaN equals nothing. Filtering
// them out is left to the callers, which handle NaN differently from one
// another; see CompareSamples and BootstrapConfidence.
func dedupeSortedCopy(gains []float64) []float64 {
	out := make([]float64, len(gains))
	copy(out, gains)
	slices.Sort(out)
	return slices.Compact(out)
}

// uniqueSortedThresholds is dedupeSortedCopy with the CompareSamples default of
// a single 0.0 threshold for empty input.
func uniqueSortedThresholds(gains []float64) []float64 {
	if len(gains) == 0 {
		return []float64{0.0}
	}
	return dedupeSortedCopy(gains)
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

// bootstrapSample returns a bootstrap sample (sampling with replacement) drawn
// from xs. The returned slice has the same length as xs and the input is not
// modified. An empty xs yields an empty sample.
//
// A non-zero prngSeed selects reproducible sampling from a DPRNG seeded with it;
// a zero prngSeed selects cryptographic randomness from a freshly built CPRNG.
//
// Index selection goes through Uint32N on either generator, which uses Lemire's multiply-shift
// reduction rather than a modulo. The residual bias is bounded by 2^-32 relative
// to the range and is negligible for sample sizes that fit in memory.
//
// This is the convenience entry point for a single sample. Callers drawing many
// samples in a loop should use bootstrapSampleSeeded or bootstrapSampleCrypto
// directly: the latter takes the generator as a parameter, so one cryptographic
// stream can serve the whole loop instead of one buffer being filled and thrown
// away per sample.
func bootstrapSample(xs []float64, prngSeed uint64) []float64 {
	if prngSeed != 0 {
		return bootstrapSampleSeeded(xs, prngSeed)
	}
	return bootstrapSampleCrypto(xs, NewCPRNG(bootstrapCPRNGBufferBytes))
}

// bootstrapCPRNGBufferBytes is the buffer size used for the cryptographic
// generator that drives unseeded bootstrap sampling.
//
// The buffer only pays off when the generator outlives a single sample. One
// draw costs 4 bytes, so this holds 2048 of them: at a hundred measurements per
// sample it serves roughly twenty samples before it has to call into
// crypto/rand again. Constructing a generator per sample instead would fill all
// 8 KiB from the OS to consume a few hundred bytes of it, which is what
// BootstrapConfidence used to do.
const bootstrapCPRNGBufferBytes = 8192

// bootstrapSampleSeeded draws a bootstrap sample from a generator freshly seeded
// with prngSeed. It is the single-sample convenience form; callers drawing many
// samples should use bootstrapSampleDPRNG with one shared generator.
func bootstrapSampleSeeded(xs []float64, prngSeed uint64) []float64 {
	rng := NewDPRNG(prngSeed)
	return bootstrapSampleDPRNG(xs, &rng)
}

// bootstrapSampleDPRNG draws a bootstrap sample from an existing deterministic
// generator, advancing it. Taking the generator as a parameter lets a caller
// draw every replicate of a run from one continuous stream.
//
// That matters for more than performance. Seeding a fresh DPRNG per replicate
// from consecutive seeds leaves a detectable trace: xorshift64 is linear over
// GF(2), so seeds two apart produce first states differing by a nearly constant
// mask. Measured over 200,000 replicates of 101 draws, that gave a serial
// correlation of 0.095 between the first index of consecutive replicates
// (noise band +/-0.007), and consecutive replicates opened with the same index
// 2.26% of the time instead of the expected 0.99%. Drawing from one stream
// removes it: the same measurement yields -0.002.
//
// The artefact never reached the confidence estimates, because those depend on
// the median of a whole sample and one correlated draw out of eleven or more
// does not move a median; effective resample counts matched the requested ones
// at every supported sample size. Using one stream is nevertheless the sounder
// construction, and it costs nothing.
func bootstrapSampleDPRNG(xs []float64, rng *DPRNG) []float64 {
	n := len(xs)
	sample := make([]float64, n)
	if n == 0 {
		return sample
	}
	for i := range n {
		sample[i] = xs[rng.Uint32N(uint32(n))]
	}
	return sample
}

// bootstrapSampleCrypto draws a bootstrap sample from an existing cryptographic
// generator. The generator is a parameter rather than a local so that callers
// performing many replicates can reuse one stream; see
// bootstrapCPRNGBufferBytes for why that matters.
//
// Sharing one stream across samples, and across both inputs of a comparison, is
// sound: the draws are consecutive values from a single cryptographic sequence
// and are therefore independent of one another.
func bootstrapSampleCrypto(xs []float64, rng *CPRNG) []float64 {
	n := len(xs)
	sample := make([]float64, n)
	if n == 0 {
		return sample
	}
	for i := range n {
		sample[i] = xs[rng.Uint32N(uint32(n))]
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
//	A map[float64]float64 where each key is a threshold from `relativeGains` and the corresponding value is
//	the estimated confidence in [0,1] that the relative speedup of A over B is at least that threshold.
//
// Repeated thresholds are collapsed before counting, so the returned confidences
// are always in [0,1]. Without that step a threshold listed n times would score
// n hits per replicate and report a confidence of n. The caller's slice is not
// modified.
//
// NaN thresholds are silently skipped and do not appear in the returned map.
// This function has no error channel, and a NaN key would be an entry no caller
// could ever look up, because NaN compares equal to nothing including itself.
// Prefer CompareSamples, which rejects NaN thresholds with a proper error.
// Infinities are kept: they behave consistently, with -Inf mapping to 1 and
// +Inf to 0.
func BootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, prngSeed uint64) map[float64]float64 {
	// Block length one is single-observation resampling: the shared core draws
	// exactly the values the dedicated sampler used to, in the same order.
	return bootstrapConfidence(A, B, relativeGains, resamples, 1, prngSeed)
}

// bootstrapConfidence is the shared implementation of BootstrapConfidence and
// BlockBootstrapConfidence. A blockLength of one gives the ordinary bootstrap.
func bootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, blockLength int, prngSeed uint64) (confidenceForThreshold map[float64]float64) {
	if blockLength == 0 {
		blockLength = AutoBlockLength(max(len(A), len(B)))
	}

	// Distinct thresholds only. Counting a repeated threshold once per
	// occurrence per replicate would push its "confidence" above 1.
	thresholds := dedupeSortedCopy(relativeGains)
	// NaN thresholds are dropped rather than reported, because this function has
	// no error channel. Keeping them would be worse than useless: delta >= NaN is
	// never true, so a NaN would score zero, and a NaN map key can be written but
	// never read back, so the entry would be unreachable for every caller.
	// CompareSamples rejects them outright.
	thresholds = slices.DeleteFunc(thresholds, math.IsNaN)

	confidenceForThreshold = make(map[float64]float64, len(thresholds))

	if resamples == 0 {
		for _, threshold := range thresholds {
			confidenceForThreshold[threshold] = math.NaN()
		}
		return confidenceForThreshold
	}

	// Counts are indexed by position rather than keyed by threshold value, so
	// that accumulation does not depend on float64 map-key behaviour.
	counts := make([]uint64, len(thresholds))

	// Both paths draw every replicate from a single generator created here.
	// Building one per sample would refill an 8 KiB crypto/rand buffer for a few
	// hundred bytes of use on the unseeded path, and would leave a measurable
	// serial correlation between replicates on the seeded one; see
	// bootstrapSampleDPRNG.
	var next func(uint32) uint32
	if prngSeed == 0 {
		cryptoRNG := NewCPRNG(bootstrapCPRNGBufferBytes)
		next = cryptoRNG.Uint32N
	} else {
		seededRNG := NewDPRNG(prngSeed)
		next = seededRNG.Uint32N
	}

	for range resamples {
		sampleA := blockSample(A, blockLength, next)
		sampleB := blockSample(B, blockLength, next)
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

		// Written out per threshold rather than stopping at the first miss.
		// The thresholds are sorted ascending, so an early exit would be
		// correct for finite values and delta, but it would also be a trap for
		// anyone later relaxing the NaN filter above: a NaN sorts to the front
		// and `delta < NaN` is false, so the scan would abort before evaluating
		// anything. The full scan costs one comparison per threshold.
		for j, threshold := range thresholds {
			if delta >= threshold {
				counts[j]++
			}
		}
	}

	for j, threshold := range thresholds {
		confidenceForThreshold[threshold] = float64(counts[j]) / float64(resamples)
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
