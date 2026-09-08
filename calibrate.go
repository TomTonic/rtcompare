package rtcompare

import (
	"fmt"
	"math"
	"runtime/debug"
)

// DefaultMaxQuantizationError is the share of the per-operation result that
// [CalibrateInnerLoops] allows the system clock's granularity to contribute,
// when CalibrationOptions.MaxQuantizationError is left at zero.
//
// One tenth of a percent is deliberately well below the harness noise floor,
// which in A/A experiments has ranged from a few tenths of a percent to over
// one. Once quantization is an order of magnitude smaller than the noise it
// stops mattering: against 0.6% of noise, adding 0.1% of quantization in
// quadrature yields 0.608%. Buying more precision than
// that only lengthens batches without making the magnitude of a difference more
// trustworthy.
//
// It is sized for that question, the magnitude of a difference, and not for the
// separate question of whether a difference exists at all. Quantization also
// collapses distinct measurements onto equal values, and equal values tie; at
// this target an ordinary candidate tied in about 15% of bootstrap replicates,
// which inflates the confidence at a threshold of zero. Tightening the target
// tenfold took that to 0.6% at ten times the batch length. See
// CollectOptions.MaxQuantizationError for the measurements.
const DefaultMaxQuantizationError = 0.001

// DefaultMaxInnerLoops caps how far [CalibrateInnerLoops] will grow the batch
// size before giving up, when CalibrationOptions.MaxInnerLoops is left at zero.
//
// The cap is generous: reaching it means one operation costs so little that a
// hundred million of them still do not fill the target batch duration, which in
// practice means the compiler removed the work rather than that the code is
// fast. See the error returned in that case.
const DefaultMaxInnerLoops uint64 = 100_000_000

// Calibration reports what [CalibrateInnerLoops] determined about a candidate.
type Calibration struct {
	// InnerLoops is the batch size to use, i.e. the value for
	// CollectOptions.InnerLoops.
	InnerLoops uint64

	// NsPerOp is a rough estimate of one operation's cost, taken from the
	// calibration batch. It is a single unreplicated measurement and is meant
	// for sanity checking, not for reporting. Use [Collect] for real numbers.
	NsPerOp float64

	// BatchDuration is how long one batch of InnerLoops operations took, in
	// nanoseconds. This is the quantity the calibration actually targets.
	BatchDuration float64

	// ClockPrecision is the smallest interval the system clock resolved, from
	// [GetSampleTimePrecision], in nanoseconds.
	ClockPrecision int64

	// QuantizationError is the relative per-operation error the clock's
	// granularity contributes at this batch size, i.e.
	// ClockPrecision/BatchDuration. It is at most the requested
	// MaxQuantizationError.
	QuantizationError float64
}

// CalibrationOptions configures [CalibrateInnerLoops]. The zero value is usable
// and selects the documented defaults.
type CalibrationOptions struct {
	// MaxQuantizationError is the largest relative per-operation error the clock
	// granularity may contribute. Zero selects [DefaultMaxQuantizationError].
	// Must be greater than zero and less than one.
	MaxQuantizationError float64

	// MaxInnerLoops caps the batch size the search will try. Zero selects
	// [DefaultMaxInnerLoops].
	MaxInnerLoops uint64

	// GCBetween requests an explicit garbage collection before every trial
	// batch, mirroring CollectOptions.GCBetween so that calibration measures
	// the same conditions the real run will use.
	GCBetween bool

	// DisableGC turns off the automatic collector for the duration of the
	// calibration, mirroring CollectOptions.DisableGC. See its documentation
	// for the caveats.
	DisableGC bool
}

// CalibrateInnerLoops determines how many operations one batch must perform so
// that the system clock's granularity contributes at most
// MaxQuantizationError of relative error to the per-operation result.
//
// This is the mechanism that makes measuring below the clock's resolution work,
// and the arithmetic behind it is simple. A single batch measurement is off by
// at most one clock tick p. Spread over n operations that becomes p/n per
// operation, so the relative error is p/(n*c) where c is the cost of one
// operation. Since n*c is just the batch duration T, the whole thing collapses
// to p/T: the error depends only on how long the batch runs, not on how fast
// the operation is. Calibration therefore searches for the smallest n whose
// batch reaches a target duration of p/MaxQuantizationError.
//
// On a machine where [GetSampleTimePrecision] reports 41 ns, the default target
// of 0.1% means batches of about 41 microseconds. On Windows, where QPC resolves
// to roughly 100 ns, the same target means about 100 microseconds per batch. In
// both cases a per-operation difference of a fraction of a nanosecond survives,
// because it is never the individual operation that is timed.
//
// The search starts at one operation per batch and grows geometrically, using
// each measurement to predict the next size, with a safety margin and a cap on
// how fast it may grow. Each candidate size is measured several times and judged
// by its shortest run, so that a batch which only reached the target because a
// scheduler hiccup stretched it is not accepted. An expensive operation may well
// calibrate to a batch size of one; the criterion is the batch duration, not the
// number of operations.
//
// The search itself is cheap, a few hundred microseconds in practice. The first
// call in a process additionally pays for [GetSampleTimePrecision], which probes
// the clock until its minimum stops improving, about 4 ms on the machine these
// notes were written on. That result is cached for the lifetime of the process,
// so it is a one-time startup cost rather than a per-call one.
//
// A failure to reach the target within MaxInnerLoops means a batch did not get
// longer as the batch size grew. In Go the usual explanation is a [Batch]
// function that ignores its n parameter, not dead code elimination: unlike some
// C and C++ compilers, the Go compiler does not remove a loop merely because
// nothing reads its result, so a loop that computes an unused value still runs
// and still takes time. Work that the compiler can fold to a constant is the
// second candidate. Writing a result to a package-level variable remains good
// practice for keeping a candidate honest, but it is not what this error is
// usually pointing at.
func CalibrateInnerLoops(c Candidate, opt CalibrationOptions) (Calibration, error) {
	if c.Batch == nil {
		return Calibration{}, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", c.label("under calibration"))
	}
	if math.IsNaN(opt.MaxQuantizationError) || opt.MaxQuantizationError < 0 {
		return Calibration{}, fmt.Errorf("rtcompare: MaxQuantizationError must be in (0,1), got %v", opt.MaxQuantizationError)
	}
	if opt.MaxQuantizationError == 0 {
		opt.MaxQuantizationError = DefaultMaxQuantizationError
	}
	if opt.MaxQuantizationError >= 1 {
		return Calibration{}, fmt.Errorf("rtcompare: MaxQuantizationError must be in (0,1), got %v", opt.MaxQuantizationError)
	}
	if opt.MaxInnerLoops == 0 {
		opt.MaxInnerLoops = DefaultMaxInnerLoops
	}

	if opt.DisableGC {
		previous := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(previous)
	}

	precision := GetSampleTimePrecision()
	targetNs := float64(precision) / opt.MaxQuantizationError

	// Growth control. The predicted next size is multiplied by a margin because
	// the prediction comes from a single noisy measurement, and it is capped
	// because a badly quantized early measurement can predict an absurd jump.
	const (
		growthCap    = 100
		safetyMargin = 1.2
		trials       = 3
		maxSteps     = 64
	)

	n := uint64(1)
	for step := 0; step < maxSteps; step++ {
		// Judge a size by its shortest run: the fastest observed batch is the
		// one least contaminated by interference, so a size that clears the
		// target even at its fastest is genuinely long enough.
		shortest := math.Inf(1)
		for range trials {
			if d := float64(timeBatch(c, n, opt.GCBetween)); d < shortest {
				shortest = d
			}
		}

		if shortest >= targetNs {
			return Calibration{
				InnerLoops:        n,
				NsPerOp:           shortest / float64(n),
				BatchDuration:     shortest,
				ClockPrecision:    precision,
				QuantizationError: float64(precision) / shortest,
			}, nil
		}

		if n >= opt.MaxInnerLoops {
			return Calibration{}, fmt.Errorf(
				"rtcompare: calibration failed: %d operations per batch took only %.0f ns, short of the %.0f ns needed for %.3f%% quantization error; "+
					"the usual cause is a Batch function that ignores its n parameter and therefore does not scale with the batch size, "+
					"followed by work the compiler could fold away at compile time; an operation genuinely too cheap to fill a batch at this size is rare",
				n, shortest, targetNs, opt.MaxQuantizationError*100)
		}

		factor := float64(growthCap)
		if shortest > 0 {
			factor = (targetNs / shortest) * safetyMargin
		}
		n = growInnerLoops(n, factor, opt.MaxInnerLoops)
	}

	return Calibration{}, fmt.Errorf(
		"rtcompare: calibration did not converge within %d steps; the candidate's cost per operation is not stable enough to size a batch", maxSteps)
}

// growInnerLoops scales n by factor, clamped to [n+1, limit] and to a bounded
// growth rate, so the search always makes progress and never overflows.
func growInnerLoops(n uint64, factor float64, limit uint64) uint64 {
	const growthCap = 100
	if !(factor > 1) { // also catches NaN
		factor = 2
	}
	if factor > growthCap {
		factor = growthCap
	}
	want := float64(n) * factor
	if want >= float64(limit) {
		return limit
	}
	next := uint64(want)
	if next <= n {
		next = n + 1
	}
	if next > limit {
		next = limit
	}
	return next
}
