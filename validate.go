package rtcompare

import (
	"fmt"
	"math"
	"slices"
)

// DefaultValidationRuns is the number of A/A experiments [ValidateHarness]
// performs when ValidationOptions.Runs is left at zero.
const DefaultValidationRuns = 10

// DefaultValidationLevel is the confidence level [ValidateHarness] judges false
// signals against when ValidationOptions.Level is left at zero.
const DefaultValidationLevel = 0.95

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

	// NoiseFloor is the largest relative difference observed between the two
	// sample sets of identical code, as a fraction. It is the practical
	// resolution limit of the setup: a real comparison reporting less than this
	// has measured nothing. Being a maximum over Runs, it is itself a noisy and
	// deliberately conservative statistic; see TypicalNoise for the middle of
	// the distribution.
	NoiseFloor float64

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
	// medians came out exactly equal, averaged over the runs.
	//
	// Timing measurements are quantized, so identical values are common and
	// ties with them: an unremarkable A/A run here produced 102 samples holding
	// only 35 distinct values, and tied medians in 17.8% of replicates. Since
	// the confidence at threshold zero asks whether delta >= 0, every one of
	// those ties counts as "A at least as fast", which lifts it by half the tie
	// rate. Measured against the tie-split figure the offset matched that
	// prediction to three decimals.
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
// This is a necessary condition, not a sufficient one. Clearing the noise floor
// says the difference is not obviously an artefact of the harness; it says
// nothing about whether it is caused by the code rather than by the compiler's
// layout choices or the machine's mood.
func (v HarnessValidation) Resolves(relativeDifference float64) bool {
	return math.Abs(relativeDifference) > v.NoiseFloor
}

// String renders the validation as a short multi-line report.
func (v HarnessValidation) String() string {
	return fmt.Sprintf(
		"A/A validation over %d runs at %d inner loops:\n"+
			"  noise floor        %+.3f%% (typical %+.3f%%)\n"+
			"  mean confidence    %.3f (0.500 expected, ties split)\n"+
			"  median confidence  %.3f\n"+
			"  tied replicates    %.1f%%\n"+
			"  false signals      %.1f%% at level %.2f (%.1f%% expected)",
		v.Runs, v.InnerLoops,
		v.NoiseFloor*100, v.TypicalNoise*100,
		v.MeanConfidence, v.MedianConfidence, v.TieRate*100,
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
// package has seen apparent differences of 0.6% to 0.8% reported with high
// confidence. Only an A/A experiment exposes that.
//
// The cost is Runs times that of one comparison, plus one calibration.
//
// A worked use, and the reason the API exists: measure the noise floor first,
// then require a real result to clear it.
//
//	v, err := rtcompare.ValidateHarness(fast, rtcompare.ValidationOptions{Collect: opts})
//	sa, sb, err := rtcompare.Collect(fast, slow, opts)
//	observed := 1 - rtcompare.Median(sa)/rtcompare.Median(sb)
//	if !v.Resolves(observed) {
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
	}

	absDeltas := make([]float64, len(deltas))
	for i, d := range deltas {
		absDeltas[i] = math.Abs(d)
	}

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
		NoiseFloor:       slices.Max(absDeltas),
		TypicalNoise:     Median(absDeltas),
		MeanConfidence:   sum / float64(len(confidences)),
		MedianConfidence: Median(confidences),
		TieRate:          Median(tieRates),
		FalseSignalRate:  float64(falseSignals) / float64(len(confidences)),
		Level:            opt.Level,
		Deltas:           deltas,
		Confidences:      confidences,
	}, nil
}
