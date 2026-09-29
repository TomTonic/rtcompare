package rtcompare

import (
	"fmt"
	"math"
	"slices"

	"github.com/TomTonic/rtcompare/prng"
)

// MinimumDataPoints is the fewest measurements per candidate that
// [CompareSamples], [EstimateDifference] and [Collect] accept. Below it a
// median has too few values to resample from for the bootstrap to say
// anything.
const MinimumDataPoints = 11

// ThresholdConfidence is the confidence that A beats B by at least Threshold,
// a relative difference as in [Estimate].Delta: the share of bootstrap
// replicates in which 1 - median(A)/median(B) >= Threshold.
type ThresholdConfidence struct {
	Threshold  float64 `json:"threshold"`
	Confidence float64 `json:"confidence"`
}

// Confidences holds one [ThresholdConfidence] per distinct threshold, in
// ascending order of threshold, as [CompareSamples], [BootstrapConfidence],
// [BlockBootstrapConfidence] and Report.Confidence return them.
type Confidences []ThresholdConfidence

// At returns the confidence for the given threshold, and whether it is there.
// Thresholds match when they differ by no more than a relative 1e-9, so that
// a threshold computed the way it was passed in, such as F2T(1.1) or
// 0.1+0.05, finds its entry.
func (c Confidences) At(threshold float64) (confidence float64, ok bool) {
	for _, tc := range c {
		if tc.Threshold == threshold {
			return tc.Confidence, true
		}
	}
	if math.IsInf(threshold, 0) || math.IsNaN(threshold) {
		return 0, false
	}
	for _, tc := range c {
		if math.Abs(tc.Threshold-threshold) <= 1e-9*math.Max(1, math.Abs(threshold)) {
			return tc.Confidence, true
		}
	}
	return 0, false
}

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
//     longer runtime); zero selects [DefaultResamples]. See the note in
//     `BootstrapConfidence` for guidance and literature references about choosing
//     the number of resamples.
//
// # Why the median
//
// Each replicate is summarised by its median rather than its mean or its
// smallest value, and that choice was checked against the alternative rather
// than assumed. Interference is one-sided — a disturbed batch is slower, never
// faster — which is an argument for a low quantile, on the reasoning that it
// sits below the contamination. Simulated against a known difference with
// lognormal measurement noise, 101 samples per side and 3000 trials, that
// reasoning turns out to be wrong in the ordinary case:
//
//	disturbed batches    median RMSE    p10 RMSE
//	                0%        0.0055      0.0079
//	               10%        0.0063      0.0079
//	               20%        0.0076      0.0082
//	               30%        0.0101      0.0086
//	               40%        0.0285      0.0091
//	               50%        0.0818      0.0098
//
// Below about 30% the median is the better estimator and a low quantile is
// merely noisier, because the median is already immune: with contamination on
// fewer than half the samples, the middle one is drawn from the clean part of
// the distribution. The smallest value is worse still at every rate, its
// variance being several times the median's once the noise has no hard floor.
//
// Past 40% the median degrades sharply, as it must: it is approaching the
// boundary of the contaminated region and begins jumping between clean and
// disturbed values. But that regime does not need a different estimator so much
// as a different machine, and it does not go unnoticed. In A/A simulations the
// noise floor that [ValidateHarness] reports rises from 1.2% with no
// interference to 3.1% at 30% and 20.5% at 40%, so a setup in which the median
// is failing announces itself as one whose measurements are worthless anyway.
//
// Returns the [Confidences], one entry per requested relative threshold with
// its confidence in [0,1]. If either input contains fewer than
// `MinimumDataPoints` values an error is returned.
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
func CompareSamples(measurementsA, measurementsB []float64, relativeGains []float64, resamples uint64) (Confidences, error) {
	if len(measurementsA) < MinimumDataPoints || len(measurementsB) < MinimumDataPoints {
		return nil, fmt.Errorf("not enough data points: need at least %d measurements for each input", MinimumDataPoints)
	}
	// Reject NaN thresholds here, at the boundary, while the caller's own index
	// is still available to name in the error. A NaN cannot be reported on: it
	// is never >= anything, so it would score a confidence of zero, and zero is
	// a perfectly ordinary answer that the caller could not tell apart from a
	// real one.
	for i, g := range relativeGains {
		if math.IsNaN(g) {
			return nil, fmt.Errorf(
				"relativeGains[%d] is NaN, which is not a usable threshold; note that F2T returns NaN for factors <= 0 or NaN, so check its result before passing it on", i)
		}
	}
	if resamples == 0 {
		resamples = DefaultResamples
	}
	return BootstrapConfidence(measurementsA, measurementsB, uniqueSortedThresholds(relativeGains), resamples, 0), nil
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
	return bootstrapSampleCrypto(xs, prng.NewCPRNG(bootstrapCPRNGBufferBytes))
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
	rng := prng.NewDPRNG(prngSeed)
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
func bootstrapSampleDPRNG(xs []float64, rng *prng.DPRNG) []float64 {
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
func bootstrapSampleCrypto(xs []float64, rng *prng.CPRNG) []float64 {
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
// `relativeGains` the function increments a counter when delta >= t. After all replicates it returns,
// for each threshold, the estimated confidence: the fraction of replicates meeting delta >= t.
//
// Numerical and edge-case behavior (important):
//   - If `resamples` is zero every confidence is math.NaN().
//   - If either sample median is NaN (for an empty sample), the replicate produces delta = NaN and
//     that replicate does not count as meeting any threshold.
//   - A median(B_sample) of zero with a non-zero median(A_sample) gives an infinite delta, which is
//     the honest answer and a sign that a batch fitted inside one clock tick; see relativeDelta.
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
//	The [Confidences], one entry per distinct threshold in ascending order, each with the estimated
//	confidence in [0,1] that the relative speedup of A over B is at least that threshold.
//
// Repeated thresholds are collapsed before counting, so the returned confidences
// are always in [0,1]. Without that step a threshold listed n times would score
// n hits per replicate and report a confidence of n. The caller's slice is not
// modified.
//
// NaN thresholds are silently skipped and do not appear in the result. This
// function has no error channel, and a NaN entry could never be found again,
// because NaN compares equal to nothing including itself. Prefer
// CompareSamples, which rejects NaN thresholds with a proper error.
// Infinities are kept: they behave consistently, with -Inf mapping to 1 and
// +Inf to 0.
func BootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, prngSeed uint64) Confidences {
	// Block length one is single-observation resampling: the shared core draws
	// exactly the values the dedicated sampler used to, in the same order.
	return bootstrapConfidence(A, B, relativeGains, resamples, 1, prngSeed)
}

// bootstrapConfidence is the shared implementation of BootstrapConfidence and
// BlockBootstrapConfidence. A blockLength of one gives the ordinary bootstrap.
func bootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, blockLength int, prngSeed uint64) Confidences {
	// Zero asks for the automatic length; anything negative is a caller error
	// with no sensible reading, so it takes the same route rather than reaching
	// blockSample as a nonsensical length.
	if blockLength <= 0 {
		blockLength = AutoBlockLength(max(len(A), len(B)))
	}

	// Distinct thresholds only. Counting a repeated threshold once per
	// occurrence per replicate would push its "confidence" above 1.
	thresholds := dedupeSortedCopy(relativeGains)
	// NaN thresholds are dropped rather than reported, because this function has
	// no error channel. Keeping them would be worse than useless: delta >= NaN is
	// never true, so a NaN would score zero, and no caller could look the entry
	// up again, since NaN equals nothing. CompareSamples rejects them outright.
	thresholds = slices.DeleteFunc(thresholds, math.IsNaN)

	result := make(Confidences, len(thresholds))
	for j, threshold := range thresholds {
		result[j] = ThresholdConfidence{Threshold: threshold, Confidence: math.NaN()}
	}
	if resamples == 0 {
		return result
	}

	countAtLeast(result, replicates(A, B, resamples, blockLength, prngSeed, false))
	return result
}

// countAtLeast sets each entry's confidence to the share of the replicates
// that meet its threshold. Every threshold is checked for every replicate
// rather than stopping at the first miss: the thresholds are sorted, so an
// early exit would be correct for finite values, but a NaN sorts to the front
// and would end the scan before anything was counted.
func countAtLeast(result Confidences, deltas []float64) {
	counts := make([]uint64, len(result))
	for _, delta := range deltas {
		for j := range result {
			if delta >= result[j].Threshold {
				counts[j]++
			}
		}
	}
	for j := range result {
		result[j].Confidence = float64(counts[j]) / float64(len(deltas))
	}
}

// replicates draws resamples bootstrap replicates of the relative difference,
// in the order drawn, all from one stream seeded with seed; see
// bootstrapStream. Unpaired, each replicate resamples A and B separately and
// compares their medians, 1 - median(A*)/median(B*). Paired, it resamples the
// ratios A[i]/B[i] of the pairs and takes 1 - median of them, so that a
// disturbance that hit both members of a pair alike cancels in their ratio.
// Replicates whose difference is undefined are NaN.
func replicates(A, B []float64, resamples uint64, blockLength int, seed uint64, paired bool) []float64 {
	next := bootstrapStream(seed)
	out := make([]float64, resamples)
	if paired {
		q := pairQuotients(A, B)
		for r := range out {
			out[r] = 1 - quickMedian(blockSample(q, blockLength, next))
		}
		return out
	}
	for r := range out {
		medA := quickMedian(blockSample(A, blockLength, next))
		medB := quickMedian(blockSample(B, blockLength, next))
		out[r] = relativeDelta(medA, medB)
	}
	return out
}

// pairQuotients returns A[i]/B[i] for each pair, with equal members giving
// exactly one, as two zeros or two equal infinities do in relativeDelta.
func pairQuotients(A, B []float64) []float64 {
	q := make([]float64, min(len(A), len(B)))
	for i := range q {
		if A[i] == B[i] {
			q[i] = 1
		} else {
			q[i] = A[i] / B[i]
		}
	}
	return q
}

// pairedDelta is the paired point estimate, 1 - median(A[i]/B[i]).
func pairedDelta(A, B []float64) float64 {
	return 1 - quickMedian(pairQuotients(A, B))
}

// F2T (FactorToThreshold) converts a multiplicative speedup timesFaster (e.g. 3.0 => A is 3× faster)
// to the internal relative‑reduction threshold used by CompareSamples and BootstrapConfidence.
func F2T(timesFaster float64) float64 {
	if timesFaster <= 0 || math.IsNaN(timesFaster) {
		return math.NaN()
	}
	return 1.0 - 1.0/timesFaster
}

// relativeDelta returns 1 - a/b, the relative amount by which a falls short of
// b. It is the quantity every threshold in this package is compared against.
//
// The degenerate cases:
//
//   - A NaN operand yields NaN, which is greater than or equal to nothing and so
//     meets no threshold.
//   - Equal operands yield exactly zero, including two zeros and two infinities
//     of the same sign, where the arithmetic would otherwise give NaN.
//   - A zero denominator with a non-zero numerator yields an infinity. That is
//     the honest answer, and it is also a signal: a median measurement of zero
//     means a whole batch fitted inside one tick of the clock, so the batch is
//     too short to measure at all. Sizing batches through [CalibrateInnerLoops]
//     prevents it.
//
// This last case used to be guarded by substituting a small epsilon for a
// denominator near zero, with the stated aim of keeping the result finite. That
// guard could not work. The epsilon was max(|b|*1e-12, SmallestNonzeroFloat64):
// for any non-zero b the test |b| < |b|*1e-12 is never true, so the branch never
// fired, and for b exactly zero it substituted a denormal that overflowed the
// division anyway. Reporting the infinity plainly is both simpler and more
// informative than a bound that was never enforced.
func relativeDelta(a, b float64) float64 {
	if math.IsNaN(a) || math.IsNaN(b) {
		return math.NaN()
	}
	if (a == 0 && b == 0) || a == b ||
		(math.IsInf(a, -1) && math.IsInf(b, -1)) ||
		(math.IsInf(a, 1) && math.IsInf(b, 1)) {
		return 0.0
	}
	return 1.0 - a/b
}

// AutoBlockLength selects the block length from the sample size, as
// [BlockBootstrapConfidence] does when told to choose for itself.
//
// It returns the nearest integer to the cube root of n, with a floor of one.
// That rate is the standard choice for the moving block bootstrap: the block has
// to grow with n so that dependence reaching further than one lag is eventually
// captured, and it has to grow more slowly than n so that the number of blocks
// keeps growing too. For the sample sizes this package works with it lands
// between two and five.
func AutoBlockLength(n int) int {
	if n < 1 {
		return 1
	}
	l := int(math.Round(math.Cbrt(float64(n))))
	if l < 1 {
		l = 1
	}
	if l > n {
		l = n
	}
	return l
}

// BlockBootstrapConfidence is [BootstrapConfidence] with contiguous blocks of
// observations instead of single observations, which is what makes it usable on
// measurements that are not independent of their neighbours.
//
// The ordinary bootstrap draws one observation at a time, which assumes the
// samples are exchangeable: that the order they arrived in carries nothing. Real
// timing measurements violate this, mildly but measurably. Correlated samples
// carry less information than the same number of independent ones, so a method
// that assumes independence believes it knows more than it does and produces
// confidences that are too extreme in both directions. Resampling whole blocks
// keeps neighbours together and preserves that dependence.
//
// The cost is that this is only worth paying when the dependence is strong
// enough to matter, and often it is not. Measured against A/A simulations of an
// AR(1) process, where identical inputs mean every reported difference is a
// false signal and 10% of runs should land outside a 90% band:
//
//	lag-1 rho    false signals    verdict
//	     0.00            8.0%     conservative
//	     0.08           10.0%     nominal
//	     0.20           13.5%     inflated
//	     0.40           21.7%     badly inflated
//	     0.60           33.1%     unusable
//
// Across 600 real measurement series on the machine these notes were written on,
// the lag-1 autocorrelation averaged +0.10 with a true spread between series of
// about 0.07 once the estimator's own noise is removed, putting roughly 6% of
// series above 0.2 and almost none above 0.3. At that distribution the aggregate
// inflation is a fraction of a percentage point, and blocks buy nothing. A
// busier machine, a candidate that interacts with the collector, or a shared CI
// runner can be a different story, which is why this is available and why
// [ValidateHarness] reports the autocorrelation it observed.
//
// What blocks actually recover, from the same simulation:
//
//	lag-1 rho    plain    blocks    verdict
//	     0.00     8.5%      9.4%    no harm done
//	     0.08    10.0%     10.0%    no harm done
//	     0.20    12.8%     10.9%    repaired
//	     0.40    20.6%     12.7%    much improved, still inflated
//	     0.60    32.2%     16.7%    halved, nowhere near repaired
//
// So this is a partial remedy, not a cure. It restores calibration through about
// 0.2 and merely improves matters beyond that, which is what fixed-length blocks
// can do: a block captures dependence reaching as far as its own length, and
// making it longer to capture more costs variance. At the point where a machine
// produces series correlated at 0.4 the statistics are not the problem; the
// measurement is, and a quieter machine or shorter runs will do more than any
// resampling scheme. Note also the last row of the earlier table read against
// this one: blocks that are too long for the dependence present are themselves
// mildly over-dispersed, which is why the automatic length is worth preferring
// over a guessed one.
//
// blockLength of zero selects [AutoBlockLength], and so does any negative
// value, which has no sensible reading. A blockLength of one is the ordinary
// bootstrap exactly, drawing the same values in the same order, so there is no
// reason to pass it deliberately. Blocks are overlapping and drawn uniformly
// from every valid start position, which is the moving block bootstrap of
// Künsch (1989); the last block of a replicate is truncated so that every
// replicate has exactly as many observations as the input.
//
// A blockLength longer than half the input is reduced to half. A block as long
// as the input has only one start position, so every replicate would reproduce
// the input exactly, the resampled difference would be a constant, and the
// confidence would come out as exactly 0 or 1 — indistinguishable from
// certainty, and wrong. Halving guarantees at least two blocks per replicate.
// The clamp is applied to each input separately, so inputs of different lengths
// are each handled on their own terms. There is no good reason to approach that
// bound in any case: long blocks cost variance, and [AutoBlockLength] stays far
// below it.
//
// All other behaviour, including threshold handling and the meaning of prngSeed,
// is that of [BootstrapConfidence].
func BlockBootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, blockLength int, prngSeed uint64) Confidences {
	return bootstrapConfidence(A, B, relativeGains, resamples, blockLength, prngSeed)
}

// bootstrapStream returns the generator every replicate of one bootstrap is
// drawn from: a DPRNG seeded with seed, or cryptographic randomness for seed
// zero. One generator per bootstrap, not one per sample: building one per
// sample would refill an 8 KiB crypto/rand buffer for a few hundred bytes of
// use on the unseeded path, and would leave a measurable serial correlation
// between replicates on the seeded one; see bootstrapSampleDPRNG. Sharing it
// is also what makes EstimateDifference and BlockBootstrapConfidence draw the
// same replicates for the same seed.
func bootstrapStream(seed uint64) func(uint32) uint32 {
	if seed == 0 {
		return prng.NewCPRNG(bootstrapCPRNGBufferBytes).Uint32N
	}
	rng := prng.NewDPRNG(seed)
	return rng.Uint32N
}

// blockSample draws one bootstrap replicate from xs using contiguous blocks of
// the given length, appending draws from next until the replicate is full.
//
// With blockLength 1 this reduces exactly to drawing len(xs) independent
// observations, in the same order and consuming the same number of random
// values as the plain sampler, which is what lets the ordinary bootstrap share
// this code without changing any seeded result.
func blockSample(xs []float64, blockLength int, next func(uint32) uint32) []float64 {
	n := len(xs)
	sample := make([]float64, 0, n)
	if n == 0 {
		return sample
	}
	// Clamp to a length that can still produce variation between replicates.
	// A block as long as the input has exactly one start position, so every
	// replicate would be the input itself and the resampled statistic would be
	// a constant: the confidence would come out as exactly 0 or 1 and look like
	// certainty rather than the artefact it is. Half the input guarantees at
	// least two blocks per replicate, which is also the usual requirement for
	// the moving block bootstrap to say anything.
	if blockLength < 1 {
		blockLength = 1
	}
	if maxBlock := max(1, n/2); blockLength > maxBlock {
		blockLength = maxBlock
	}
	starts := uint32(n - blockLength + 1)
	for len(sample) < n {
		start := int(next(starts))
		end := start + blockLength
		if end > n {
			end = n
		}
		if room := n - len(sample); end-start > room {
			end = start + room
		}
		sample = append(sample, xs[start:end]...)
	}
	return sample
}

// lag1Autocorrelation returns the correlation between each sample and the one
// before it, or zero for a series with no variation or fewer than two values.
func lag1Autocorrelation(xs []float64) float64 {
	if len(xs) < 2 {
		return 0
	}
	var mean float64
	for _, v := range xs {
		mean += v
	}
	mean /= float64(len(xs))

	var numerator, denominator float64
	for i := range len(xs) - 1 {
		numerator += (xs[i] - mean) * (xs[i+1] - mean)
	}
	for _, v := range xs {
		denominator += (v - mean) * (v - mean)
	}
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}
