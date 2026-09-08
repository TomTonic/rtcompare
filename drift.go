package rtcompare

import (
	"fmt"
	"math"
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
