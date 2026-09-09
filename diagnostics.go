package rtcompare

// This file holds the two diagnostics that sit alongside the main comparison:
// DetectDrift, which checks whether a run's own order carries a trend the
// bootstrap cannot see, and EstimateDifference, which reports the size of a
// difference with an interval instead of a confidence against a threshold.

import (
	"fmt"
	"math"
	"slices"
	"sort"
)

// DriftReport describes whether a series of measurements trended over the
// course of the run that produced them.
type DriftReport struct {
	// N is the number of samples examined.
	N int

	// Spearman is the rank correlation between each sample and its position in
	// the run, in [-1,1]. Positive means later measurements tended to be larger,
	// that is, the machine grew slower as the run went on.
	//
	// Ranks are used rather than the values themselves because timing samples
	// are heavy-tailed: a single preempted batch would dominate a correlation
	// computed on raw values. Tied values receive their average rank.
	Spearman float64

	// Z is Spearman standardized by its null distribution, Spearman*sqrt(N-1),
	// which is approximately standard normal when no trend exists.
	Z float64

	// PValue is the two-sided probability of seeing a rank correlation at least
	// this strong if the samples were in fact exchangeable, i.e. if the order in
	// which they were measured carried no information.
	PValue float64

	// FirstHalf and SecondHalf are the medians of the first and last N/2
	// samples, with the middle sample excluded when N is odd. RelativeShift is
	// their difference over FirstHalf, giving the size of the drift where PValue
	// gives its significance. A drift can be highly significant and too small to
	// care about, or large and indistinguishable from noise.
	FirstHalf     float64
	SecondHalf    float64
	RelativeShift float64
}

// Drifted reports whether the trend is significant at the given level, e.g.
// 0.05. It says nothing about whether the trend is large enough to matter; read
// RelativeShift for that.
func (d DriftReport) Drifted(level float64) bool {
	return d.PValue < level
}

// String renders the report as one line.
func (d DriftReport) String() string {
	return fmt.Sprintf("drift over %d samples: rho %+.3f, z %+.2f, p %.4f, shift %+.3f%%",
		d.N, d.Spearman, d.Z, d.PValue, d.RelativeShift*100)
}

// DetectDrift looks for a monotone trend across a series of measurements taken
// in run order, such as one of the slices returned by [Collect].
//
// This answers a question the bootstrap structurally cannot. Resampling treats
// the samples as an unordered bag and asks how much the estimate would move if
// they were drawn again; it discards the order in which they arrived, so a
// machine that grew steadily slower during the run leaves no trace in its
// output. That is precisely the situation in which a comparison is most
// misleading, because the drift is charged to whichever candidate was measured
// later.
//
// The test is Spearman's rank correlation between each sample and its position,
// standardized as rho*sqrt(N-1), which is approximately standard normal under
// the null hypothesis that the samples are exchangeable. Ranks rather than
// values, because a single preempted batch is an enormous outlier and would
// otherwise dominate; tied values, which are common in quantized timings, take
// their average rank.
//
// Significance and size are reported separately and should be read separately.
// A long run resolves a drift of a fraction of a percent as highly significant,
// which is worth knowing but may be far too small to affect a conclusion. The
// converse also happens.
//
// The false positive rate was checked rather than assumed, because timing
// samples are quantized and heavily tied, and a rank test on tied data is not
// obviously well behaved. On synthetic series collapsed onto 3, 8 and 35
// distinct values it fired at 4.9%, 3.9% and 3.9% against a nominal 5% with a
// standard error of 0.4, so ties do not break it and it errs slightly
// conservative. On real timing series with their order randomly permuted, which
// preserves the value distribution exactly while destroying any trend, it fired
// at 4.80% over 6000 permutations drawn from 600 series, a 95% interval of
// [4.27%, 5.33%] that covers the nominal rate. Permutations of one series are
// not independent of one another by construction, so that is the cluster-robust
// interval; it happens to match the naive binomial one almost exactly, the
// design effect being 0.95.
//
// Those same 600 series, left in the order they were measured, tripped the test
// in 13.0% of runs, a 95% interval of [10.3%, 15.7%]. The gap to the permuted
// rate is 8.2 percentage points at z = 5.9. Their mean lag-1 autocorrelation was
// +0.083, with a 95% interval of [+0.069, +0.096] built from the spread actually
// observed across series rather than one assumed from the null. So measurement
// series on an ordinary machine do carry order structure, and it is not a quirk
// of how their values are distributed.
// Whether a given series carries a slow trend or short-range correlation between
// neighbours is not something this test separates; both make the samples
// non-exchangeable, which is what the bootstrap assumes they are.
//
// An error is returned for fewer than four samples, or if any sample is not
// finite. Note that power is poor below roughly twenty samples: a real drift can
// easily go unnoticed there, so a large PValue from a short series is weak
// evidence of calm rather than evidence of no drift.
func DetectDrift(samples []float64) (DriftReport, error) {
	n := len(samples)
	if n < 4 {
		return DriftReport{}, fmt.Errorf("rtcompare: need at least 4 samples to look for a trend, got %d", n)
	}
	for i, v := range samples {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return DriftReport{}, fmt.Errorf("rtcompare: samples[%d] is not finite (%v)", i, v)
		}
	}

	ranks := midranks(samples)

	// Correlate the ranks against position. Position is 0..n-1 without ties, so
	// its mean and variance are known in closed form.
	nf := float64(n)
	meanPos := (nf - 1) / 2
	var meanRank float64
	for _, r := range ranks {
		meanRank += r
	}
	meanRank /= nf

	var cov, varPos, varRank float64
	for i, r := range ranks {
		dp := float64(i) - meanPos
		dr := r - meanRank
		cov += dp * dr
		varPos += dp * dp
		varRank += dr * dr
	}

	report := DriftReport{N: n}
	if varRank == 0 || varPos == 0 {
		// Every sample identical: no trend can be said to exist.
		report.PValue = 1
	} else {
		report.Spearman = cov / math.Sqrt(varPos*varRank)
		report.Z = report.Spearman * math.Sqrt(nf-1)
		// P(|Z| > z) for a standard normal is erfc(z/sqrt(2)).
		report.PValue = math.Erfc(math.Abs(report.Z) / math.Sqrt2)
	}

	half := n / 2
	first := append([]float64(nil), samples[:half]...)
	second := append([]float64(nil), samples[n-half:]...)
	report.FirstHalf = Median(first)
	report.SecondHalf = Median(second)
	if report.FirstHalf != 0 {
		report.RelativeShift = (report.SecondHalf - report.FirstHalf) / report.FirstHalf
	}

	return report, nil
}

// midranks returns the rank of each element, averaging the ranks of tied
// values. Ranks are 1-based, so the smallest of n distinct values gets 1.
func midranks(xs []float64) []float64 {
	n := len(xs)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	sort.Slice(order, func(a, b int) bool { return xs[order[a]] < xs[order[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && xs[order[j+1]] == xs[order[i]] {
			j++
		}
		// Positions i..j inclusive share a value; give them the average of the
		// 1-based ranks i+1 .. j+1.
		avg := float64(i+j)/2 + 1
		for k := i; k <= j; k++ {
			ranks[order[k]] = avg
		}
		i = j + 1
	}
	return ranks
}

// DefaultConfidenceLevel is the interval level [EstimateDifference] uses when
// asked for zero.
const DefaultConfidenceLevel = 0.95

// Estimate is a point estimate of the relative difference between two sets of
// measurements, together with an interval around it.
//
// It answers a different question from [CompareSamples]. That one takes
// thresholds and reports how confident one can be that each is met, which is
// what you want when a threshold is given: a release gate, a regression budget.
// This one reports how large the difference appears to be and how precisely
// that is known, which is what you want when no threshold is given and the
// honest answer might be "somewhere between 2% and 19%".
type Estimate struct {
	// Delta is the relative difference computed on the measurements themselves,
	// 1 - median(A)/median(B). Positive means A is smaller, which for runtimes
	// means faster. It is the point estimate, taken from the data rather than
	// from the resampling, so it does not move when Resamples changes.
	Delta float64

	// Low and High bound Delta at the requested Level. They are the empirical
	// quantiles of the resampled deltas, i.e. a percentile bootstrap interval.
	Low, High float64

	// Level is the interval's coverage level, e.g. 0.95.
	Level float64

	// Resamples is how many bootstrap replicates the interval was built from.
	Resamples uint64

	// BootstrapMedian is the median of the resampled deltas. Comparing it with
	// Delta shows the resampling bias: a large gap means the statistic behaves
	// awkwardly on this data and the interval deserves suspicion.
	BootstrapMedian float64
}

// Excludes reports whether the interval lies entirely on one side of the given
// value, i.e. whether the data rule that value out at this level. Excludes(0)
// asks whether a difference has been established at all.
func (e Estimate) Excludes(value float64) bool {
	return (e.Low > value && e.High > value) || (e.Low < value && e.High < value)
}

// String renders the estimate as one line, in percent.
func (e Estimate) String() string {
	return fmt.Sprintf("%+.2f%% [%+.2f%%, %+.2f%%] at %.0f%% confidence",
		e.Delta*100, e.Low*100, e.High*100, e.Level*100)
}

// EstimateDifference reports how much smaller the measurements in A are than
// those in B, as a relative fraction, with a bootstrap interval around it.
//
// The point estimate is computed on the measurements directly. The interval is
// a percentile bootstrap: resample both inputs, recompute the difference for
// each replicate, and take the empirical quantiles at (1-level)/2 and
// (1+level)/2. Ties in the resampled distribution are handled by the quantile
// definition below, which interpolates.
//
// Coverage was measured rather than assumed, by simulating pairs drawn from
// distributions whose true difference is known and counting how often the
// interval contained it. At a nominal 95%, over 2000 trials per cell with 1000
// resamples, the standard error being half a point:
//
//	samples per side    normal    lognormal    one-sided contamination
//	              11     97.2%        97.0%                      96.9%
//	              25     96.7%        96.9%                      96.7%
//	              51     96.5%        96.2%                      96.8%
//	             101     96.0%        96.4%                      96.3%
//
// The interval is therefore conservative by one to two points rather than
// optimistic, consistently across shapes and sizes, and it narrows towards
// nominal only slowly. The reason is discreteness: a resampled median can only
// take values that appear in the sample, so the bootstrap distribution of the
// median is coarser than its true sampling distribution and its quantiles sit
// further apart. Erring wide is the safe direction, but a stated 95% is closer
// to 96 or 97 in practice.
//
// The misses split evenly between the two ends, about 1.8% on each side against
// a nominal 2.5%, so the interval is well centred and not merely shifted.
//
// A caveat that no interval width can express: this covers sampling
// uncertainty only. It says nothing about a bias that affected every
// measurement, and a machine that drifted during the run will produce a tight
// interval around the wrong number. Read it alongside [ValidateHarness] and
// [DetectDrift].
//
// Level zero selects [DefaultConfidenceLevel]. Resamples zero selects
// [DefaultResamples]. An error is returned if either input holds fewer than
// [MinimumDataPoints] values or if level is not strictly between zero and one.
func EstimateDifference(A, B []float64, level float64, resamples uint64) (Estimate, error) {
	// Block length one is single-observation resampling, which is what this
	// function has always done; [Compare] uses the shared core with longer
	// blocks when it measures dependence between neighbouring samples.
	return estimateDifference(A, B, level, resamples, 1)
}

// estimateDifference is the shared implementation of EstimateDifference and the
// interval [Compare] builds. A blockLength of one gives the ordinary bootstrap.
func estimateDifference(A, B []float64, level float64, resamples uint64, blockLength int) (Estimate, error) {
	if uint64(len(A)) < MinimumDataPoints || uint64(len(B)) < MinimumDataPoints {
		return Estimate{}, fmt.Errorf("not enough data points: need at least %d measurements for each input", MinimumDataPoints)
	}
	if level == 0 {
		level = DefaultConfidenceLevel
	}
	if math.IsNaN(level) || level <= 0 || level >= 1 {
		return Estimate{}, fmt.Errorf("rtcompare: level must be strictly between 0 and 1, got %v", level)
	}
	if resamples == 0 {
		resamples = DefaultResamples
	}

	// QuickMedian rearranges what it is given, so the point estimate works on
	// copies and leaves the caller's measurements alone.
	pointA := QuickMedian(slices.Clone(A))
	pointB := QuickMedian(slices.Clone(B))

	deltas := make([]float64, 0, resamples)
	rng := NewCPRNG(bootstrapCPRNGBufferBytes)
	for range resamples {
		medA := QuickMedian(blockSample(A, blockLength, rng.Uint32N))
		medB := QuickMedian(blockSample(B, blockLength, rng.Uint32N))
		if d := relativeDelta(medA, medB); !math.IsNaN(d) {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return Estimate{}, fmt.Errorf("rtcompare: every bootstrap replicate produced an undefined difference; the measurements are probably not usable")
	}
	slices.Sort(deltas)

	tail := (1 - level) / 2
	return Estimate{
		Delta:           relativeDelta(pointA, pointB),
		Low:             quantileOfSorted(deltas, tail),
		High:            quantileOfSorted(deltas, 1-tail),
		Level:           level,
		Resamples:       resamples,
		BootstrapMedian: quantileOfSorted(deltas, 0.5),
	}, nil
}

// quantileOfSorted returns the p-quantile of an ascending slice, interpolating
// linearly between the two neighbouring order statistics.
//
// Interpolation matters here because the resampled deltas are discrete: with
// quantized measurements the replicates pile up on a handful of values, and
// picking a nearest order statistic would make an interval bound jump in steps
// rather than move smoothly with the level.
func quantileOfSorted(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return math.NaN()
	}
	if n == 1 {
		return sorted[0]
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 1 {
		return sorted[n-1]
	}
	pos := p * float64(n-1)
	lower := int(math.Floor(pos))
	upper := lower + 1
	if upper >= n {
		return sorted[n-1]
	}
	frac := pos - float64(lower)
	return sorted[lower]*(1-frac) + sorted[upper]*frac
}
