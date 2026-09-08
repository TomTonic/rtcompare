package rtcompare

import "math"

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
// blockLength of zero selects [AutoBlockLength]. A blockLength of one is the
// ordinary bootstrap exactly, drawing the same values in the same order, so
// there is no reason to pass it deliberately. Blocks are overlapping and drawn
// uniformly from every valid start position, which is the moving block bootstrap
// of Künsch (1989); the last block of a replicate is truncated so that every
// replicate has exactly as many observations as the input.
//
// All other behaviour, including threshold handling and the meaning of prngSeed,
// is that of [BootstrapConfidence].
func BlockBootstrapConfidence(A, B []float64, relativeGains []float64, resamples uint64, blockLength int, prngSeed uint64) map[float64]float64 {
	return bootstrapConfidence(A, B, relativeGains, resamples, blockLength, prngSeed)
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
	if blockLength < 1 || blockLength > n {
		blockLength = n
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
