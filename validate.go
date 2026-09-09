package rtcompare

import (
	"fmt"
	"math"
	"slices"
)

// DefaultValidationRuns is the number of A/A experiments [ValidateHarness]
// performs when ValidationOptions.Runs is left at zero.
//
// It is sized for the rates the report quotes, not for the noise floor, which
// needs far fewer runs. FalseSignalRate and DriftRate are proportions estimated
// from Runs observations, so their standard error is sqrt(p(1-p)/Runs). At the
// nominal 10% that FalseSignalRate is compared against, ten runs give a
// standard error of 9.5 percentage points, which is as large as the quantity
// being estimated: a perfectly calibrated setup would print 0.0% about 35% of
// the time and 20% or more about 26% of the time, and neither number would mean
// anything. Forty runs bring that to 4.7 points, which is enough to tell a
// calibrated setup from a badly broken one, though still not enough to resolve
// small departures.
//
// The cost is linear in Runs and small in absolute terms. On the machine these
// notes were written on, validating a candidate calibrated to 48 microsecond
// batches took 0.76 s at ten runs and 3.03 s at forty. Validation is a thing
// done once per setup, not once per comparison, so that is a good trade.
const DefaultValidationRuns = 40

// NoiseFloorQuantile is the quantile of the observed A/A differences that
// HarnessValidation.NoiseFloor reports.
//
// The floor used to be the maximum, which turned out to be the wrong statistic
// for the job. A sample maximum has no population value to converge on: it
// grows with the number of runs, without limit. Measured here, eight repetitions
// per row, the same candidate throughout:
//
//	Runs    max (old floor)         90th percentile
//	   5              0.779%                  0.493%
//	  10              0.208%                  0.169%
//	  20              0.230%                  0.144%
//	  40              0.313%                  0.165%
//	  80              0.356%                  0.166%
//
// The max climbs steadily from twenty runs onward while the quantile settles;
// an independent earlier run reproduced the same climb, 0.236% to 0.491% over
// Runs 5 to 40. The five-run row is unreliable in both columns, one repetition
// there having caught a disturbed machine. The spread across repetitions tells
// the same story: at eighty runs the max ranged over 0.168% to 0.834% while the
// quantile ranged over 0.164% to 0.168%, a band forty times tighter.
//
// That mattered in practice, because [HarnessValidation.Resolves] compares
// against this number. With a maximum, validating more carefully raised the bar,
// so the natural response to an uncertain result — run it again with more runs —
// made the gate stricter rather than better informed. A quantile converges, so
// the gate stops moving once there are enough runs to estimate it.
//
// The price is that this is no longer an observed bound. Roughly one A/A run in
// ten exceeds it, by construction. HarnessValidation.MaxObservedNoise still
// reports the largest difference actually seen, for anyone who wants it.
const NoiseFloorQuantile = 0.90

// DefaultValidationLevel is the confidence level [ValidateHarness] judges false
// signals against when ValidationOptions.Level is left at zero.
const DefaultValidationLevel = 0.95

// DriftLevel is the significance level at which [ValidateHarness] counts a run
// as having drifted. It is a conventional 5%: on data with no trend the drift
// test fires this often by construction, so DriftRate is only interesting when
// it sits well above this.
const DriftLevel = 0.05

// HarnessValidation reports what repeated A/A experiments revealed about a
// measurement setup: the same candidate measured as both A and B, under exactly
// the options a real comparison would use.
//
// Everything an A/A experiment finds is noise by construction, because there is
// no difference to find. That makes it the one experiment that can say how much
// of an apparent difference a setup invents on its own.
type HarnessValidation struct {
	// Runs is how many A/A experiments were performed.
	Runs int

	// InnerLoops is the batch size used, after calibration if it was requested.
	// It is fixed across runs so that they are comparable.
	InnerLoops uint64

	// NoiseFloor is the [NoiseFloorQuantile] quantile of the absolute relative
	// differences observed between the two sample sets of identical code, as a
	// fraction. It is the practical resolution limit of the setup: a real
	// comparison reporting less than this has measured nothing.
	//
	// It is a quantile rather than the maximum because a maximum has no value to
	// converge on and grows with Runs, which made the gate stricter the more
	// carefully a setup was validated. See NoiseFloorQuantile for the
	// measurements. The consequence to keep in mind is that this is not a bound:
	// about one A/A run in ten exceeds it by construction, so clearing it is
	// evidence rather than proof. See MaxObservedNoise for the largest
	// difference actually seen and TypicalNoise for the middle of the
	// distribution.
	NoiseFloor float64

	// MaxObservedNoise is the largest absolute relative difference seen across
	// the runs. It is what NoiseFloor used to report. Read it as the worst case
	// this validation happened to catch, remembering that it grows with Runs and
	// so describes the length of the validation as much as the setup.
	MaxObservedNoise float64

	// TypicalNoise is the median absolute relative difference across runs.
	TypicalNoise float64

	// MeanConfidence and MedianConfidence average the confidence that "A is
	// faster than B" across the runs. Since A and B are the same code, an
	// unbiased setup must centre on 0.5. A systematic departure indicates that
	// something about the measurement favours one position over the other,
	// which is what interleaving the order is meant to prevent.
	//
	// These are computed with ties split rather than as the plain confidence at
	// threshold zero, which would centre above 0.5 even on a perfect setup. See
	// TieRate.
	MeanConfidence   float64
	MedianConfidence float64

	// TieRate is the share of bootstrap replicates in which both resampled
	// medians came out exactly equal, taken as the median across the runs.
	//
	// The median rather than the mean, because of how the per-run figure is
	// obtained. It is recovered as confAB + confBA - 1 from two independent
	// bootstrap runs, so it carries the Monte Carlo error of both and can come
	// out slightly negative when the true rate is near zero. Clamping those to
	// zero would bias a mean upwards by a few tenths of a point on a setup that
	// ties hardly at all; the median is unaffected, since half the estimates
	// land on either side. The cost is that a true rate below the Monte Carlo
	// noise reports as zero, which is the honest answer at that resolution.
	//
	// Timing measurements are quantized, so identical values are common and ties
	// with them. One A/A run here produced 102 samples holding only 35 distinct
	// values, and tied medians in 14.4% of its replicates. Since the confidence
	// at threshold zero asks whether delta >= 0, every one of those ties counts
	// as "A at least as fast", which lifts it by half the tie rate. Over 40 runs
	// of that setup the mean tie rate was 17.8% and the offset against the
	// tie-split figure was 0.089, matching half of it to three decimals.
	//
	// A high tie rate means the measurement is coarse relative to the
	// differences being asked about. The fix is longer batches, and only longer
	// batches. One measurement is an integer count of clock ticks divided by the
	// batch size, so its granularity is precision/InnerLoops: raising InnerLoops
	// makes the individual value finer and ties correspondingly rarer. Raising
	// Repeats does not, and measurably does not; it draws more values from the
	// same coarse set.
	//
	// Measured on one candidate here, holding Repeats at 51 and varying only the
	// batch size:
	//
	//	InnerLoops    granularity    tie rate
	//	     1,000     0.042 ns/op       86.3%
	//	     5,000     0.008 ns/op       15.0%
	//	   100,000     0.0004 ns/op       1.5%
	//	   400,000     0.0001 ns/op       0.0%
	//
	// and varying only Repeats at a fixed batch size of 20,000, tie rates of
	// 2.2%, 2.3%, 2.8% and 1.8% for 21, 51, 101 and 201 repeats: no trend.
	//
	// In practice the knob to turn is CollectOptions.MaxQuantizationError, which
	// is what sizes the batch. See its documentation for what the default costs
	// here.
	TieRate float64

	// FalseSignalRate is the fraction of runs whose confidence fell outside
	// [1-Level, Level], that is, runs that would have reported a difference in
	// one direction or the other where none exists. Under perfect calibration
	// it should come to about 2*(1-Level).
	FalseSignalRate float64

	// Level is the confidence level FalseSignalRate was judged against.
	Level float64

	// DriftRate is the fraction of runs in which at least one of the two sample
	// series showed a significant trend across the run, as judged by
	// [DetectDrift] at [DriftLevel].
	//
	// A trend means the machine did not hold still while it was being measured,
	// which is the one situation where measurement order turns into an apparent
	// difference between candidates. It is also invisible to the bootstrap,
	// which treats the samples as an unordered bag. On a quiet machine this
	// should sit near DriftLevel itself, that being the rate at which the test
	// fires on calm data by construction; substantially above it means runs are
	// long enough for the machine to change during them.
	//
	// Note that DetectDrift looks for a monotone trend but will also respond to
	// strong short-range correlation between neighbouring batches. Both violate
	// the exchangeability the bootstrap assumes, so either is worth knowing
	// about, but this figure does not distinguish them.
	DriftRate float64

	// MedianDriftShift is the median *absolute* relative shift between the first
	// and second half of a run's samples, across the runs and both series. It is
	// the size of the drift where DriftRate is its prevalence.
	//
	// Absolute, so it does not matter whether a run sped up or slowed down; both
	// are the machine failing to hold still, and averaging them signed would let
	// them cancel. It is therefore never negative, and it carries no direction.
	// Read DriftReport.RelativeShift from [DetectDrift] on an individual series
	// if the direction matters.
	MedianDriftShift float64

	// Autocorrelation is the median lag-1 autocorrelation of the sample series,
	// across the runs and both candidates. It says how much each measurement
	// resembles the one taken before it.
	//
	// Its use is deciding whether the ordinary bootstrap can be trusted here.
	// Resampling assumes the samples are exchangeable, and correlated samples
	// carry less information than the same number of independent ones, so beyond
	// some point a method that assumes independence grows overconfident. The
	// point is around 0.2: in AR(1) simulations the rate of false signals from
	// identical inputs held at its nominal 10% up to 0.08, reached 13.5% at 0.2,
	// 21.7% at 0.4 and 33.1% at 0.6. Below roughly 0.2 there is nothing to fix;
	// above it, see [BlockBootstrapConfidence].
	//
	// For reference, 600 real series on the machine these notes were written on
	// averaged +0.10, with about 6% of them genuinely above 0.2 once the noise of
	// the estimator itself is accounted for.
	Autocorrelation float64

	// Deltas and Confidences hold the per-run values the summary is built from,
	// in the order the runs were performed. Their sequence is worth a look:
	// a trend across them is drift rather than noise.
	Deltas      []float64
	Confidences []float64
}

// Resolves reports whether a relative difference of the given size is larger
// than the noise this validation observed, and so whether the setup can tell it
// apart from nothing at all. The sign is ignored.
//
// This is a necessary condition, not a sufficient one, in two separate ways.
// Clearing the floor says the difference is not obviously an artefact of the
// harness; it says nothing about whether it is caused by the code rather than by
// the compiler's layout choices or the machine's mood. And the floor is the
// [NoiseFloorQuantile] quantile rather than a bound, so about one A/A run in ten
// produces a difference that would clear it. Treat a result just above the floor
// as unresolved and a result well above it as resolved.
//
// The floor is also measured on one candidate against itself. Two genuinely
// different candidates can be noisier than that, since they need not allocate
// alike or occupy the cache alike, so validate both and use the worse of the two
// floors.
func (v HarnessValidation) Resolves(relativeDifference float64) bool {
	return math.Abs(relativeDifference) > v.NoiseFloor
}

// String renders the validation as a short multi-line report.
func (v HarnessValidation) String() string {
	return fmt.Sprintf(
		"A/A validation over %d runs at %d inner loops:\n"+
			"  noise floor        %.3f%% (typical %.3f%%, worst seen %.3f%%)\n"+
			"  mean confidence    %.3f (0.500 expected, ties split)\n"+
			"  median confidence  %.3f\n"+
			"  tied replicates    %.1f%%\n"+
			"  drifting runs      %.1f%% (median |shift| %.3f%%)\n"+
			"  autocorrelation    %+.3f (blocks worthwhile above ~0.2)\n"+
			"  false signals      %.1f%% at level %.2f (%.1f%% expected)",
		v.Runs, v.InnerLoops,
		v.NoiseFloor*100, v.TypicalNoise*100, v.MaxObservedNoise*100,
		v.MeanConfidence, v.MedianConfidence, v.TieRate*100,
		v.DriftRate*100, v.MedianDriftShift*100, v.Autocorrelation,
		v.FalseSignalRate*100, v.Level, 2*(1-v.Level)*100)
}

// ValidationOptions configures [ValidateHarness]. The zero value is usable.
type ValidationOptions struct {
	// Collect holds the measurement options to validate. Pass the same options
	// the real comparison will use: a noise floor measured under different
	// conditions describes a different setup. If its InnerLoops is zero it is
	// calibrated once and then held fixed for every run.
	Collect CollectOptions

	// Runs is the number of A/A experiments to perform. Zero selects
	// [DefaultValidationRuns]. At least two are required for a floor to mean
	// anything.
	Runs int

	// Resamples is the bootstrap resample count per run. Zero selects
	// [DefaultResamples].
	Resamples uint64

	// Level is the confidence level that FalseSignalRate is judged against.
	// Zero selects [DefaultValidationLevel]. Must be in (0.5, 1).
	Level float64
}

// ValidateHarness measures how much difference a setup reports between two
// measurements of identical code, and returns it as a [HarnessValidation].
//
// It runs the candidate against itself through [Collect], repeatedly, using the
// options supplied. Any difference it finds is by definition an artefact: of
// measurement order, of drift, of the scheduler, of the collector. The result
// is therefore the floor below which that setup cannot distinguish a real
// difference from its own noise, and it is the honest companion to any
// confidence figure the same setup produces.
//
// This matters because bootstrap resampling cannot supply it. Resampling
// quantifies how much the estimate would move if the same measurements were
// drawn again; it cannot see a bias that affected every measurement equally, and
// it will report a tight confidence around one. Measured on identical code, this
// package has seen apparent differences ranging from a few tenths of a percent
// to well over one, carried with high confidence. Only an A/A experiment
// exposes that.
//
// The cost is Runs times one [Collect] plus one calibration, and rather more
// bootstrap work than a single comparison: each run resamples twice, once in
// each direction, because splitting ties needs the confidence both ways. The
// resampling dominates. Measured on a candidate calibrated to 48 microsecond
// batches, the whole validation took 0.76 s at ten runs and 3.03 s at forty, of
// which the measurement itself was under a tenth.
//
// A worked use, and the reason the API exists: measure the noise floor first,
// then require a real result to clear it.
//
// Validate both candidates, not just one: they need not be equally well
// behaved, and a comparison is only as trustworthy as the worse of them.
//
//	va, err := rtcompare.ValidateHarness(fast, rtcompare.ValidationOptions{Collect: opts})
//	vb, err := rtcompare.ValidateHarness(slow, rtcompare.ValidationOptions{Collect: opts})
//	floor := max(va.NoiseFloor, vb.NoiseFloor)
//
//	sa, sb, err := rtcompare.Collect(fast, slow, opts)
//	observed := 1 - rtcompare.Median(sa)/rtcompare.Median(sb)
//	if math.Abs(observed) <= floor {
//	    // The difference is within what this machine invents on its own.
//	}
func ValidateHarness(c Candidate, opt ValidationOptions) (HarnessValidation, error) {
	if c.Batch == nil {
		return HarnessValidation{}, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", c.label("under validation"))
	}
	if opt.Runs < 0 {
		return HarnessValidation{}, fmt.Errorf("rtcompare: Runs must not be negative, got %d", opt.Runs)
	}
	if opt.Runs == 0 {
		opt.Runs = DefaultValidationRuns
	}
	if opt.Runs < 2 {
		return HarnessValidation{}, fmt.Errorf("rtcompare: Runs must be at least 2 for a noise floor to mean anything, got %d", opt.Runs)
	}
	if opt.Resamples == 0 {
		opt.Resamples = DefaultResamples
	}
	if opt.Level == 0 {
		opt.Level = DefaultValidationLevel
	}
	if opt.Level <= 0.5 || opt.Level >= 1 {
		return HarnessValidation{}, fmt.Errorf("rtcompare: Level must be in (0.5, 1), got %v", opt.Level)
	}

	co := opt.Collect
	if co.InnerLoops == 0 {
		// Calibrate once. Recalibrating per run would let the batch size drift
		// between runs and make their noise levels incomparable.
		cal, err := CalibrateInnerLoops(c, CalibrationOptions{
			MaxQuantizationError: co.MaxQuantizationError,
			MaxInnerLoops:        co.MaxInnerLoops,
			GCBetween:            co.GCBetween,
			DisableGC:            co.DisableGC,
		})
		if err != nil {
			return HarnessValidation{}, fmt.Errorf("calibrating candidate %s: %w", c.label("under validation"), err)
		}
		co.InnerLoops = cal.InnerLoops
	}

	deltas := make([]float64, 0, opt.Runs)
	confidences := make([]float64, 0, opt.Runs)
	tieRates := make([]float64, 0, opt.Runs)
	driftShifts := make([]float64, 0, 2*opt.Runs)
	autocorrelations := make([]float64, 0, 2*opt.Runs)
	driftedRuns := 0

	for run := range opt.Runs {
		sampleA, sampleB, err := Collect(c, c, co)
		if err != nil {
			return HarnessValidation{}, fmt.Errorf("A/A run %d of %d: %w", run+1, opt.Runs, err)
		}
		medA, medB := Median(sampleA), Median(sampleB)
		delta := 0.0
		if medB != 0 && !math.IsNaN(medA) && !math.IsNaN(medB) {
			delta = 1 - medA/medB
		}
		deltas = append(deltas, delta)

		// Both directions, so that ties can be split. With
		//   confAB = P(medA < medB) + P(tie)
		//   confBA = P(medA > medB) + P(tie)
		// and the three probabilities summing to one, (confAB + 1 - confBA)/2
		// collapses to P(medA < medB) + P(tie)/2, and confAB + confBA - 1
		// recovers the tie rate. Both follow from the public API alone.
		confAB := BootstrapConfidence(sampleA, sampleB, []float64{0.0}, opt.Resamples, 0)[0.0]
		confBA := BootstrapConfidence(sampleB, sampleA, []float64{0.0}, opt.Resamples, 0)[0.0]
		confidences = append(confidences, (confAB+1-confBA)/2)
		tieRates = append(tieRates, math.Max(0, confAB+confBA-1))

		// Drift is a property of the order the samples arrived in, which the
		// bootstrap above has already discarded. Both series are examined; a run
		// counts as drifting if either did.
		drifted := false
		for _, series := range [][]float64{sampleA, sampleB} {
			d, err := DetectDrift(series)
			if err != nil {
				// Too few samples to look for a trend, or a non-finite value.
				// Neither is a reason to fail the validation.
				continue
			}
			driftShifts = append(driftShifts, math.Abs(d.RelativeShift))
			autocorrelations = append(autocorrelations, lag1Autocorrelation(series))
			if d.Drifted(DriftLevel) {
				drifted = true
			}
		}
		if drifted {
			driftedRuns++
		}
	}

	// Sorted so that the floor can be read off as a quantile. This is a private
	// copy, so the caller's Deltas keep the order the runs were performed in.
	absDeltas := make([]float64, len(deltas))
	for i, d := range deltas {
		absDeltas[i] = math.Abs(d)
	}
	slices.Sort(absDeltas)

	var sum float64
	falseSignals := 0
	for _, conf := range confidences {
		sum += conf
		if conf > opt.Level || conf < 1-opt.Level {
			falseSignals++
		}
	}

	return HarnessValidation{
		Runs:             opt.Runs,
		InnerLoops:       co.InnerLoops,
		NoiseFloor:       quantileOfSorted(absDeltas, NoiseFloorQuantile),
		MaxObservedNoise: absDeltas[len(absDeltas)-1],
		TypicalNoise:     Median(absDeltas),
		MeanConfidence:   sum / float64(len(confidences)),
		MedianConfidence: Median(confidences),
		TieRate:          Median(tieRates),
		FalseSignalRate:  float64(falseSignals) / float64(len(confidences)),
		Level:            opt.Level,
		DriftRate:        float64(driftedRuns) / float64(opt.Runs),
		MedianDriftShift: medianOrZero(driftShifts),
		Autocorrelation:  medianOrZero(autocorrelations),
		Deltas:           deltas,
		Confidences:      confidences,
	}, nil
}

// medianOrZero is Median with an empty input mapping to zero rather than to the
// zero Median itself returns, so that callers need not distinguish them.
func medianOrZero(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	return Median(xs)
}
