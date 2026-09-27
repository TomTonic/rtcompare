package rtcompare

import (
	"fmt"
	"math"
	"runtime/metrics"
	"strings"
	"time"
)

// AutocorrelationThreshold is the lag-1 autocorrelation above which [Compare]
// stops treating the samples as independent and resamples them in blocks.
//
// Below it there is nothing to repair and blocks only cost variance; above it
// the ordinary bootstrap believes it has more information than it does. The
// value comes from AR(1) simulations in which the rate of false signals from
// identical inputs held at its nominal 10% up to a correlation of 0.08, reached
// 13.5% at 0.2 and 21.7% at 0.4. See [BlockBootstrapConfidence] for the full
// tables.
const AutocorrelationThreshold = 0.2

// SuspendThreshold is how far the wall clock may run ahead of the monotonic
// clock during a [Compare] before the machine is taken to have been suspended.
//
// On Linux and macOS the monotonic clock stops while the machine sleeps and the
// wall clock does not, so the gap between them is the time spent asleep. A
// second is far above what clock adjustments produce in the span of one
// comparison and far below the idle sleeps that have actually paused
// comparisons, which lasted 15 to 25 minutes.
const SuspendThreshold = time.Second

// LargeHeapThreshold is the live heap above which [Compare] warns that a
// single process's result is not to be trusted on its own.
//
// Above roughly the size of a last-level cache, where the data lies in memory
// starts to decide how fast it is, and that placement is fixed for a process
// and different in the next. Measured on 1M-node lists, processes scattered 4.5
// times as widely as their own intervals said. The threshold sits at the lower
// end of common last-level caches, so that the warning comes early rather than
// late; it is a hint drawn from the heap as a whole, not a measurement of what
// the candidates touch, and can fire for a program that holds much memory the
// candidates never read.
const LargeHeapThreshold = 16 << 20

// CompareOptions configures [Compare]. The zero value is usable and selects the
// documented defaults throughout.
type CompareOptions struct {
	// Collect holds the measurement options, and its documentation is the place
	// to look for the knobs that matter: batch sizing, measurement order,
	// garbage collection. Leaving InnerLoops at zero, which is the default,
	// lets the batches be sized automatically.
	Collect CollectOptions

	// Thresholds are relative speedups to report a confidence for, e.g. 0.05 for
	// "at least 5% faster". Optional: when empty, Report.Confidence is nil and
	// the estimated difference and its interval are the whole answer. Use these
	// when a threshold is given to you, such as a regression budget.
	Thresholds []float64

	// ValidationRuns is the number of A/A experiments per candidate. Zero
	// selects [DefaultValidationRuns]; anything else must be at least two, since
	// a floor cannot be estimated from one observation. Ignored when
	// SkipValidation is set.
	//
	// This is the dominant cost of a comparison, and lowering it costs
	// precision in exactly the figures that justify the result. See
	// [DefaultValidationRuns] for what the rates are worth at a given count.
	ValidationRuns int

	// SkipValidation omits the A/A experiments. They are what turns a confident
	// number into a trustworthy one, so this is worth setting only when the
	// noise floor of this exact setup is already known, or in a test that cares
	// about speed rather than truth. Report.Warnings says so when it is set.
	SkipValidation bool

	// Level is the coverage level of the reported interval. Zero selects
	// [DefaultConfidenceLevel].
	Level float64

	// Resamples is the bootstrap resample count. Zero selects
	// [DefaultResamples].
	Resamples uint64
}

// Report is everything [Compare] found, with Resolved and Warnings as the short
// answer and the rest as the evidence for it.
type Report struct {
	// NsPerOpA and NsPerOpB are the median per-operation costs, in the units the
	// batch functions produced, which for timing candidates is nanoseconds.
	NsPerOpA, NsPerOpB float64

	// SamplesA and SamplesB are the raw measurements in the order they were
	// taken, for anyone who wants to do their own analysis.
	SamplesA, SamplesB []float64

	// Estimate is how much smaller A is than B, with an interval around it.
	// Positive means A is faster.
	//
	// The interval covers the noise within this one process and nothing else.
	// Each process gets its own memory layout, and for data larger than the
	// caches or full of pointers that layout alone can shift the difference by
	// several points, far beyond this interval, and differently in the next
	// process. Where that matters, run the comparison in several processes and
	// read [Combine]'s pooled interval instead; see the multiproc package.
	Estimate Estimate

	// Confidence maps each requested threshold to the confidence that it is
	// met. It is nil when CompareOptions.Thresholds was empty.
	Confidence map[float64]float64

	// Validated records whether the A/A experiments were performed. When false,
	// NoiseFloor is zero because it is unknown, not because it is small.
	Validated bool

	// ValidationA and ValidationB are the A/A results for each candidate.
	ValidationA, ValidationB HarnessValidation

	// NoiseFloor is the worse of the two candidates' noise floors, and so the
	// difference this setup can invent from identical code. A result that does
	// not clear it has resolved nothing.
	NoiseFloor float64

	// Autocorrelation is the worse of the two candidates' lag-1
	// autocorrelations, and is what BlockLength was chosen from.
	Autocorrelation float64

	// BlockLength is the resampling block length that was used. One means the
	// samples were treated as independent, which is the ordinary bootstrap.
	BlockLength int

	// DriftA and DriftB test each series for a trend across the run. A zero N
	// means the test could not be run.
	DriftA, DriftB DriftReport

	// LiveHeap is the live heap in bytes as of the last garbage collection
	// during the comparison, zero if the runtime did not report it. Above
	// [LargeHeapThreshold] the warnings recommend running the comparison in
	// several processes.
	LiveHeap uint64

	// Suspended is how much longer the wall clock ran than the monotonic clock
	// during the comparison, when that exceeded [SuspendThreshold], and zero
	// otherwise. Non-zero means the machine was asleep for about that long in
	// the middle of the measurement, which neither the samples nor the drift
	// tests can show reliably. See SuspendThreshold for the platforms this
	// works on.
	Suspended time.Duration

	// DriftRatio tests the ratio B/A of each pair of neighbouring batches for a
	// trend across the run. A zero N means the test could not be run.
	//
	// It sees what DriftA and DriftB cannot: a machine that slows down slows
	// both candidates alike and leaves the ratio flat, while one candidate that
	// started with the caches to itself and loses them over the run moves the
	// ratio and nothing else. A trend here means the two had not reached a
	// steady state when the measurement began, so the difference depends on how
	// long the run was; see [DefaultWarmupDuration].
	DriftRatio DriftReport

	// Resolved is the short answer: the difference is both statistically
	// distinguishable from zero and larger than what this setup invents on its
	// own. It is deliberately conservative, and false does not mean the
	// candidates are equally fast; it means this run did not establish that they
	// are not.
	Resolved bool

	// Warnings lists everything that undermines the result, in plain sentences.
	// An empty slice is the good case. They are worth reading even when Resolved
	// is true.
	Warnings []string
}

// String renders the report as a short multi-line summary.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "A %.4g per op, B %.4g per op\n", r.NsPerOpA, r.NsPerOpB)
	fmt.Fprintf(&b, "difference %s\n", r.Estimate)

	if r.Validated {
		fmt.Fprintf(&b, "noise floor %.3f%%, autocorrelation %+.3f", r.NoiseFloor*100, r.Autocorrelation)
	} else {
		fmt.Fprintf(&b, "noise floor not measured, autocorrelation %+.3f", r.Autocorrelation)
	}
	if r.BlockLength > 1 {
		fmt.Fprintf(&b, ", resampled in blocks of %d", r.BlockLength)
	}
	b.WriteString("\n")

	switch {
	case r.Resolved && r.Estimate.Delta > 0:
		b.WriteString("resolved: A is faster than B\n")
	case r.Resolved:
		b.WriteString("resolved: A is slower than B\n")
	default:
		b.WriteString("not resolved: this run did not establish a difference\n")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(&b, "  warning: %s\n", w)
	}
	for _, t := range sortedKeys(r.Confidence) {
		fmt.Fprintf(&b, "  confidence that A beats B by %.2f%%: %.1f%%\n", t*100, r.Confidence[t]*100)
	}
	return strings.TrimRight(b.String(), "\n")
}

// sortedKeys returns the map's keys in ascending order, so that a report reads
// the same way every time it is printed.
func sortedKeys(m map[float64]float64) []float64 {
	if len(m) == 0 {
		return nil
	}
	keys := make([]float64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return dedupeSortedCopy(keys)
}

// Compare measures two candidates and answers, in one call, whether one is
// genuinely faster than the other on this machine.
//
// It exists because the honest procedure has several steps and three judgement
// calls, and getting any of them wrong quietly produces a confident wrong
// answer. Compare performs the whole protocol and makes the judgement calls from
// what it measured:
//
//  1. Size the batches once, so that the clock contributes at most a fixed share
//     of error, and hold that size fixed for everything that follows. This is
//     what makes differences far below the clock's resolution measurable.
//  2. Run each candidate against itself, repeatedly, to find out what this setup
//     reports as a difference when there is provably none. That is the noise
//     floor, and it is the number every result has to be read against. The two
//     candidates' validations are interleaved batch by batch, see
//     [ValidatePair], so that neither enters the comparison with the caches to
//     itself.
//  3. Warm both candidates up, alternately, for at least
//     CollectOptions.WarmupDuration, then measure them against each other,
//     interleaved.
//  4. Test each series, and the ratio between them, for a trend across the run,
//     which resampling cannot see because it discards the order the samples
//     arrived in.
//  5. Resample in blocks if the measurements turned out to be correlated with
//     their neighbours, and as single observations if they did not.
//  6. Report the difference, an interval around it, and whether it clears both
//     zero and the noise floor.
//
// Both candidates are validated, not just one, because they need not be equally
// well behaved and the comparison is only as trustworthy as the worse of them.
// The batch size is determined before validation rather than inside it, so that
// the floor describes the same setup the measurement used; calibrating
// separately would let them differ.
//
// Report.Resolved is the short answer and Report.Warnings is the fine print.
// Read the warnings even when Resolved is true: a run can clear both bars and
// still have drifted.
//
// # Cost
//
// Validation dominates, at CompareOptions.ValidationRuns experiments per
// candidate against one measurement run. Comparing this package's own two median
// implementations at every default took about six seconds. Set SkipValidation to
// pay only for the measurement, accepting that the result then has nothing to be
// read against.
//
// Two warm-ups of CollectOptions.WarmupDuration come on top, one before the
// validation and one before the measurement, which at [DefaultWarmupDuration] is
// 0.6 s in all, or 0.3 s with SkipValidation.
//
// # What ran before it
//
// Whatever touched one candidate's data last before Compare, such as building
// its fixture, starts that candidate with a warm cache. The warm-ups are there
// to wash this out, and with a working set near the size of the last-level
// cache they need the time they take. Building the two candidates' data in an
// interleaved or balanced order costs nothing and removes the question. When a
// head start survives anyway, Report.DriftRatio shows it as a trend and
// Report.Warnings says so.
//
// # What it still cannot tell you
//
// The measured difference is that of the whole batch body, not of the isolated
// function, so any fixed per-operation overhead in the loop shrinks it; see the
// note on attenuation in [Collect]. And a noise floor measured on identical code
// is a lower bound on the noise between two different ones. Neither is repaired
// by more resamples.
//
// An error is returned if either candidate has a nil Batch, if batch sizing
// fails, if any threshold is NaN, or if the underlying measurement or
// validation fails. See [Collect] for the option-validation errors.
func Compare(a, b Candidate, opt CompareOptions) (Report, error) {
	start := time.Now()
	if a.Batch == nil {
		return Report{}, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", a.label("A"))
	}
	if b.Batch == nil {
		return Report{}, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", b.label("B"))
	}
	for i, t := range opt.Thresholds {
		if math.IsNaN(t) {
			return Report{}, fmt.Errorf(
				"rtcompare: Thresholds[%d] is NaN, which is not a usable threshold; note that F2T returns NaN for factors <= 0 or NaN", i)
		}
	}
	if opt.Resamples == 0 {
		opt.Resamples = DefaultResamples
	}
	if opt.Level == 0 {
		opt.Level = DefaultConfidenceLevel
	}

	co := opt.Collect

	// Size the batches once and hold the result fixed. Validation and
	// measurement have to describe the same setup, and leaving InnerLoops at
	// zero would have each of the three runs calibrate for itself.
	if co.InnerLoops == 0 {
		var err error
		co.InnerLoops, err = calibratePair(a, b, CalibrationOptions{
			MaxQuantizationError: co.MaxQuantizationError,
			MaxInnerLoops:        co.MaxInnerLoops,
			GCBetween:            co.GCBetween,
			DisableGC:            co.DisableGC,
		})
		if err != nil {
			return Report{}, err
		}
	}

	r := Report{BlockLength: 1}

	if !opt.SkipValidation {
		// Together rather than one after the other: a candidate validated on
		// its own would enter the measurement with the caches to itself.
		vo := ValidationOptions{Collect: co, Runs: opt.ValidationRuns, Resamples: opt.Resamples}
		va, vb, err := ValidatePair(a, b, vo)
		if err != nil {
			return Report{}, fmt.Errorf("validating candidates %s and %s: %w", a.label("A"), b.label("B"), err)
		}
		r.Validated = true
		r.ValidationA, r.ValidationB = va, vb
		r.NoiseFloor = math.Max(va.NoiseFloor, vb.NoiseFloor)
		r.Autocorrelation = math.Max(va.Autocorrelation, vb.Autocorrelation)
	}

	sa, sb, err := Collect(a, b, co)
	if err != nil {
		return Report{}, err
	}
	r.SamplesA, r.SamplesB = sa, sb
	r.NsPerOpA, r.NsPerOpB = Median(sa), Median(sb)

	if d, err := DetectDrift(sa); err == nil {
		r.DriftA = d
	}
	if d, err := DetectDrift(sb); err == nil {
		r.DriftB = d
	}
	if d, err := DetectDrift(pairRatios(sa, sb)); err == nil {
		r.DriftRatio = d
	}

	// Without validation there is no A/A estimate of the dependence, so fall
	// back to the run itself. It is the same quantity measured on one sample
	// instead of many, which is noisier but better than assuming independence.
	if !r.Validated {
		r.Autocorrelation = math.Max(lag1Autocorrelation(sa), lag1Autocorrelation(sb))
	}
	if r.Autocorrelation > AutocorrelationThreshold {
		r.BlockLength = AutoBlockLength(min(len(sa), len(sb)))
	}

	est, err := estimateDifference(sa, sb, opt.Level, opt.Resamples, r.BlockLength)
	if err != nil {
		return Report{}, err
	}
	r.Estimate = est

	if len(opt.Thresholds) > 0 {
		r.Confidence = bootstrapConfidence(sa, sb, opt.Thresholds, opt.Resamples, r.BlockLength, 0)
	}

	// Both bars have to be cleared: the difference must be distinguishable from
	// zero, and it must be larger than what this setup invents from identical
	// code. Neither implies the other.
	r.Resolved = est.Excludes(0) && math.Abs(est.Delta) > r.NoiseFloor
	r.Suspended = suspendedSince(start)
	r.LiveHeap = liveHeap()
	r.Warnings = r.warnings()
	return r, nil
}

// suspendedSince returns how much further the wall clock has moved than the
// monotonic clock since start, if that exceeds SuspendThreshold. Round(0)
// strips the monotonic reading, which leaves the two subtractions measuring
// the same interval on the two clocks.
func suspendedSince(start time.Time) time.Duration {
	return suspendGap(time.Now().Round(0).Sub(start.Round(0)), time.Since(start))
}

// liveHeap reads the live heap as of the last garbage collection. It is used
// rather than the heap in use, which includes garbage not yet collected, and
// rather than forcing a collection, which would be a side effect of Compare.
func liveHeap() uint64 {
	sample := []metrics.Sample{{Name: "/gc/heap/live:bytes"}}
	metrics.Read(sample)
	if sample[0].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64()
}

// suspendGap is the decision behind suspendedSince, apart from the clocks so
// that it can be tested.
func suspendGap(wall, monotonic time.Duration) time.Duration {
	if gap := wall - monotonic; gap > SuspendThreshold {
		return gap
	}
	return 0
}

// pairRatios returns b[i]/a[i] for each pair of samples taken next to each
// other, which is the series a head start of one candidate shows up in. A zero
// in a produces a non-finite ratio, which DetectDrift then declines.
func pairRatios(a, b []float64) []float64 {
	ratios := make([]float64, min(len(a), len(b)))
	for i := range ratios {
		ratios[i] = b[i] / a[i]
	}
	return ratios
}

// resolution is the smallest relative change that could move this report's
// conclusion: the larger of the noise floor and half the interval's width. It
// exists so that a trend is only worth a warning when it is large enough to
// matter to the question asked, not merely significant.
func (r Report) resolution() float64 {
	return math.Max(r.NoiseFloor, (r.Estimate.High-r.Estimate.Low)/2)
}

// warnings lists the things that undermine a report, in plain sentences.
func (r Report) warnings() []string {
	var w []string

	if !r.Validated {
		w = append(w, "validation was skipped, so the noise floor is unknown; the difference below has nothing to be read against")
	} else if math.Abs(r.Estimate.Delta) <= r.NoiseFloor {
		w = append(w, fmt.Sprintf(
			"the difference of %.2f%% does not clear the %.2f%% noise floor, which is what this setup reports between two runs of identical code",
			r.Estimate.Delta*100, r.NoiseFloor*100))
	}

	if !r.Estimate.Excludes(0) {
		w = append(w, fmt.Sprintf(
			"the interval [%.2f%%, %.2f%%] includes zero, so a difference in either direction is consistent with these measurements",
			r.Estimate.Low*100, r.Estimate.High*100))
	}

	if r.LiveHeap > LargeHeapThreshold {
		w = append(w, fmt.Sprintf(
			"the program holds %d MB of live data, more than the caches of many machines; for data that size, where it lies in memory can shift the result by several points in ways the interval does not cover, and differently in the next process; run the comparison in several processes with the multiproc package",
			r.LiveHeap>>20))
	}

	if r.Suspended > 0 {
		w = append(w, fmt.Sprintf(
			"the machine appears to have been suspended for %s during the comparison; its samples straddle the pause, so repeat it with the machine kept awake",
			r.Suspended.Round(time.Second)))
	}

	// Drift is a warning rather than a veto: interleaving the measurement order
	// means a trend hits both candidates about equally, so it inflates the
	// spread more than it biases the comparison. It is only worth a warning
	// when it is large enough to matter to this result; a long run finds
	// shifts of a tenth of a percent significant, and a warning that fires on
	// nearly every run tells nobody anything.
	for _, d := range []struct {
		name string
		rep  DriftReport
	}{{"A", r.DriftA}, {"B", r.DriftB}} {
		if d.rep.N > 0 && d.rep.Drifted(DriftLevel) && math.Abs(d.rep.RelativeShift) > r.resolution() {
			w = append(w, fmt.Sprintf(
				"candidate %s drifted during the run, shifting %+.2f%% from its first half to its second; the machine did not hold still",
				d.name, d.rep.RelativeShift*100))
		}
	}

	// Unlike drift in one series, a trend in the ratio is a bias: it means one
	// candidate was warmer than the other for part of the run.
	if d := r.DriftRatio; d.N > 0 && d.Drifted(DriftLevel) && math.Abs(d.RelativeShift) > r.resolution() {
		w = append(w, fmt.Sprintf(
			"the ratio B/A shifted %+.2f%% from the first half of the run to the second, so the candidates had not reached a steady state and the difference depends on how long the run was; "+
				"a common cause is a head start for whichever candidate ran alone last before the comparison or had its data built last, which a longer CollectOptions.WarmupDuration removes",
			d.RelativeShift*100))
	}

	if r.Validated {
		if tie := math.Max(r.ValidationA.TieRate, r.ValidationB.TieRate); tie > 0.05 {
			w = append(w, fmt.Sprintf(
				"%.1f%% of bootstrap replicates tied, so the measurement is coarse relative to the question; lower CollectOptions.MaxQuantizationError to lengthen the batches",
				tie*100))
		}
		if fs := math.Max(r.ValidationA.FalseSignalRate, r.ValidationB.FalseSignalRate); fs > 0.25 {
			w = append(w, fmt.Sprintf(
				"in %.0f%% of A/A runs this setup reported a difference between identical code, well above the %.0f%% expected; treat any confidence from it with suspicion",
				fs*100, 2*(1-r.ValidationA.Level)*100))
		}
	}

	return w
}
