package rtcompare

import (
	"fmt"
	"math"
	"runtime"
	"runtime/debug"
	"time"
)

// Batch runs the code under test exactly n times.
//
// The batch function owns its inner loop. This is deliberate and is the central
// design decision of [Collect]: the library resolves the function value once per
// batch, not once per operation. Two consequences follow.
//
// First, the cost of the indirect call is amortized over n operations and is
// therefore negligible (measured at roughly 0.01 ns/op for n = 20000). Second,
// and more importantly, the compiler's optimization boundary sits at the batch
// body rather than around every single operation. Code inside the batch is
// inlined, hoisted and register-allocated the same way it would be in
// production. A per-operation callback would forbid inlining of the candidate
// entirely and would measure a call boundary that the real program does not have.
//
// A batch function must perform exactly n units of the work under test and must
// not perform work whose cost depends on anything other than n. Work that is not
// part of the comparison belongs in [Candidate.Setup], which runs outside the
// measured region.
type Batch func(n uint64)

// Candidate bundles one implementation under test with its per-batch lifecycle.
//
// Setup and Teardown exist so that preparation and cleanup can be kept out of
// the measured region. Anything done inside the [Batch] function is measured,
// including work the caller only needs in order to make the measurement possible
// at all: allocating a working buffer, restoring state that the previous batch
// mutated, building an input corpus. Charging that to the candidate is what
// causes attenuation, see the note in [Collect].
//
// The distinction that matters is per-batch versus per-operation. Per-batch
// preparation can be hoisted into Setup and disappears from the measurement
// entirely. Per-operation preparation cannot: if the code under test mutates its
// input, the input has to be refreshed inside the loop, and its cost is measured
// along with the candidate. Setup does not solve that case, and no harness can.
//
// Setup and Teardown also run around the warm-up batches, so that the warm-up
// exercises exactly the same code path as the measurement.
//
// A caveat that Setup makes easy to introduce accidentally: the two candidates'
// setups leave the machine in different states before their measured regions
// begin. If one candidate's Setup allocates a large buffer and the other's does
// not, the first candidate starts with a colder cache and more GC pressure, and
// that difference is charged to the candidate rather than to its Setup. Keep the
// two setups comparable in cost and allocation behaviour, or move the asymmetric
// part outside [Collect] entirely.
type Candidate struct {
	// Name is an optional label used in error messages. It has no effect on
	// measurement.
	Name string

	// Setup runs before every batch, outside the measured region and before the
	// optional garbage collection requested by CollectOptions.GCBetween, so that
	// garbage produced by Setup is collected before the clock starts. May be nil.
	Setup func()

	// Batch is the code under test. Must not be nil.
	Batch Batch

	// Teardown runs after every batch, outside the measured region. May be nil.
	Teardown func()
}

// label returns a human-readable identifier for error messages.
func (c Candidate) label(position string) string {
	if c.Name == "" {
		return position
	}
	return fmt.Sprintf("%s (%q)", position, c.Name)
}

// Order selects the sequence in which the two candidates are measured within
// each repeat.
//
// Measurement order is a classical source of systematic bias. If the machine
// changes over the course of a run, through thermal throttling, frequency
// scaling or page cache warm-up, then a fixed A-then-B order turns that change
// into an apparent difference between the candidates, because one of them is
// always measured in the later, altered conditions. Interleaving removes the
// mechanism, and it is free: the order is decided before the clock is read.
//
// Whether it matters on a given machine is a separate question, and one worth
// checking rather than assuming. Across 40 A/A experiments here, no strategy was
// measurably biased: mean confidences of 0.487 (Sequential), 0.475 (ABBA) and
// 0.517 (Random), each within one standard error of about 0.04 of the 0.5 that
// an unbiased setup must produce. Treat interleaving as insurance with a zero
// premium rather than as a demonstrated correction, and use [ValidateHarness] to
// find out what your own machine and options actually do.
type Order int

const (
	// OrderABBA alternates the order every repeat: A then B on even repeats,
	// B then A on odd repeats. Over each block of four batches both candidates
	// occupy the same mean position in the run and are each preceded by the
	// other exactly half the time, which cancels a first-order trend across the
	// run as well as carry-over between neighbouring batches. It is the zero
	// value, so the arrangement that removes the mechanism is what you get
	// without asking.
	OrderABBA Order = iota

	// OrderRandom picks the order independently at random for each repeat,
	// driven by CollectOptions.Seed for reproducibility. Use it when the
	// interference you are worried about might itself be periodic and could
	// alias with the strict alternation of ABBA; prefer OrderABBA otherwise,
	// since its balance is exact rather than expected.
	OrderRandom

	// OrderSequential always measures A before B. It is the naive arrangement,
	// kept for comparison and for regression testing: it is the one order in
	// which a trend across the run maps directly onto an apparent difference
	// between the candidates. Prefer OrderABBA for results.
	OrderSequential
)

// String implements [fmt.Stringer].
func (o Order) String() string {
	switch o {
	case OrderABBA:
		return "ABBA"
	case OrderRandom:
		return "Random"
	case OrderSequential:
		return "Sequential"
	default:
		return fmt.Sprintf("Order(%d)", int(o))
	}
}

// DefaultRepeats is the number of timing samples [Collect] gathers per candidate
// when CollectOptions.Repeats is left at zero.
//
// It is comfortably above [MinimumDataPoints], so that the bootstrap has enough
// distinct values to resample from, and it is odd, so that the median is one
// of the measured values rather than the mean of the two in the middle.
//
// Note that raising this does not make a coarse measurement finer. More repeats
// draw more values from the same quantized set; only a longer batch adds
// resolution. See CollectOptions.MaxQuantizationError.
const DefaultRepeats = 101

// DefaultWarmup is the least number of unmeasured batches [Collect] runs per
// candidate before collecting samples, when CollectOptions.Warmup is left at
// zero. The warm-up also runs for at least [DefaultWarmupDuration], which is the
// bound that usually decides how long it takes.
const DefaultWarmup = 1

// DefaultWarmupDuration is the least wall-clock time [Collect] spends warming
// up both candidates, together, before it collects samples, when
// CollectOptions.WarmupDuration is left at zero.
//
// A fixed number of batches is not a warm-up when the working set is about the
// size of the last-level cache. Whatever ran alone last before the measurement,
// be it an A/A validation, a calibration or the caller's own fixture build,
// leaves the cache full of its own data. Calibrated batches are short, often a
// fraction of a millisecond, and a whole run of them may touch too little memory
// to turn the cache over, so the head start survives into the medians. Two
// identical 16 MB pointer chases on a machine with 32 MB of L3 measured the
// candidate that ran alone last 5 to 50% faster after one warm-up pair, and
// within noise of each other after about 400 pairs of 0.3 ms batches, some
// 250 ms in all. See issue #111 and the reproduction in cmd/rtcompare-aa.
//
// Time is the bound rather than a batch count because what has to happen, the
// cache turning over, takes a roughly fixed amount of work, and batches are
// calibrated to a duration rather than to a volume. The cost is paid once per
// [Collect] and once per validation, not once per validation run; see
// [ValidateHarness].
const DefaultWarmupDuration = 300 * time.Millisecond

// CollectOptions configures a [Collect] run.
//
// Every count in this struct is a Go int with the usual Go convention for
// counts: zero selects the documented default, and a negative value is a
// programming error rather than a mode. Negative values are rejected with an
// error instead of being silently clamped, because in practice they arise from
// arithmetic at the call site (a subtraction that underflowed) and failing
// loudly is more useful than measuring something unintended. An unsigned type
// would turn exactly those bugs into enormous positive counts instead.
type CollectOptions struct {
	// Repeats is the number of timing samples to collect per candidate.
	// Zero selects [DefaultRepeats]. Must be at least [MinimumDataPoints],
	// because that is what CompareSamples requires of its inputs.
	Repeats int

	// InnerLoops is the number of operations each batch invocation performs,
	// i.e. the n handed to the [Batch] function.
	//
	// This value is what makes measurement below the system clock's resolution
	// possible: the quantization error of a single batch is at most the clock's
	// granularity, so per operation it shrinks to granularity/InnerLoops. With a
	// 100 ns clock (Windows QPC) and InnerLoops = 20000 the residual quantization
	// error is 0.005 ns/op. Differences far below one clock tick are recoverable
	// this way; a per-operation difference of 1.89 ns was recovered to within
	// 0.07 percentage points against a 41 ns clock floor.
	//
	// Zero calibrates the value automatically via [CalibrateInnerLoops], sizing
	// batches so that the clock contributes at most MaxQuantizationError of
	// relative error. Both candidates are calibrated and the larger of the two
	// results is used for both, so that each batch is at least long enough and
	// both candidates run the same number of operations. Note that the first
	// calibration in a process pays for [GetSampleTimePrecision].
	InnerLoops uint64

	// MaxQuantizationError bounds the relative per-operation error the clock's
	// granularity may contribute when InnerLoops is calibrated automatically.
	// Zero selects [DefaultMaxQuantizationError]. Ignored when InnerLoops is set
	// explicitly.
	//
	// It has a second effect worth knowing about. Quantization does not only
	// blur a measurement, it also collapses distinct measurements onto the same
	// value, and equal values produce equal medians. That matters for the
	// confidence at threshold zero, which asks whether delta >= 0 and so counts
	// every tie as "A at least as fast". Measured on one candidate here:
	//
	//	MaxQuantizationError   InnerLoops   batch      tie rate
	//	                0.01          387   4.5 us       100.0%
	//	               0.001         4072    46 us        15.3%   (the default)
	//	              0.0001        44710   482 us         0.6%
	//
	// The default is sized for accuracy of a difference's magnitude, where it
	// performs well. If the question is instead "is A faster at all", tighten it
	// by an order of magnitude and pay ten times the batch length for it.
	// [ValidateHarness] reports the tie rate a setup actually produces.
	MaxQuantizationError float64

	// MaxInnerLoops caps the batch size automatic calibration will try. Zero
	// selects [DefaultMaxInnerLoops]. Ignored when InnerLoops is set explicitly.
	MaxInnerLoops uint64

	// Order selects the measurement order within each repeat. The zero value
	// is [OrderABBA].
	Order Order

	// Warmup is the least number of unmeasured batches run per candidate before
	// sample collection starts, to fault in pages, grow stacks and train branch
	// predictors and caches. Zero selects [DefaultWarmup]. To run no warm-up at
	// all, set SkipWarmup rather than passing a negative number.
	//
	// The warm-up continues until both this count and WarmupDuration have been
	// reached, and it alternates the order of the candidates the way the
	// measurement does, so that neither of them ends it with the caches to
	// itself.
	Warmup int

	// WarmupDuration is the least wall-clock time the warm-up runs for, across
	// both candidates together. Zero selects [DefaultWarmupDuration], whose
	// documentation explains why a count alone is not enough. Negative values
	// are rejected. To warm up by count alone, set it to [time.Nanosecond].
	WarmupDuration time.Duration

	// SkipWarmup disables warm-up entirely, both the count and the duration.
	// This exists as its own field so that Warmup keeps a single unambiguous
	// meaning; "no warm-up" is a mode, not a count. Measuring without warm-up
	// means the first samples include one-time costs such as page faults and
	// stack growth, and that whichever candidate ran alone last before Collect
	// starts with a warm cache.
	SkipWarmup bool

	// GCBetween requests an explicit garbage collection before every batch,
	// after Setup and outside the measured region. This reduces the chance that
	// a collection triggered by one candidate's allocations lands inside the
	// other candidate's measured region. It is applied symmetrically to both
	// candidates. An explicit collection runs even when DisableGC is set.
	//
	// Measured on an allocating candidate in an A/A setup, over 8 runs of 101
	// samples each, by the relative standard deviation of the samples:
	//
	//	neither flag      157.3 ns/op   spread 10.26%
	//	GCBetween         139.5 ns/op   spread  6.10%
	//	DisableGC         132.3 ns/op   spread  5.93%
	//	both              139.4 ns/op   spread  5.12%
	//
	// Both flags together roughly halve the spread. Note what the ns/op column
	// shows about DisableGC on its own, and see its documentation.
	GCBetween bool

	// DisableGC turns off the automatic garbage collector for the duration of
	// the Collect call via [debug.SetGCPercent], restoring the previous setting
	// before returning. Combined with GCBetween this gives fully deterministic
	// collection points: never during a measured region, always between them.
	//
	// Three caveats. The setting is process-global, so it affects any other
	// goroutine running concurrently with Collect. The heap is not collected
	// automatically while it is in effect, so an allocation-heavy candidate over
	// many repeats can grow memory use substantially. And it removes GC assist
	// work from allocating candidates, which makes them look faster than they
	// would be in a program where the collector is running: in the measurement
	// tabulated under GCBetween, an allocating candidate ran at 132.3 ns/op with
	// the collector disabled versus 157.3 ns/op with it enabled, a 16%
	// difference that exists only in the harness. That is an acceptable trade
	// when comparing two algorithms and a misleading one when estimating what a
	// service will do, so it is off by default.
	//
	// Do not use it alone. Without GCBetween the heap grows monotonically over
	// the run, which turns into drift across the samples; in the same A/A
	// measurement, DisableGC on its own produced the largest spurious difference
	// of all four configurations. Pair it with GCBetween so that collection
	// happens at deterministic points between measured regions.
	DisableGC bool

	// Seed drives the order decisions when Order is [OrderRandom]. Zero selects
	// a non-deterministic seed. Set it to a fixed non-zero value to reproduce a
	// run's measurement order exactly.
	Seed uint64
}

// Collect measures two candidate implementations and returns one timing sample
// per repeat for each, in nanoseconds per operation. The returned slices are
// suitable as direct inputs to [CompareSamples] or [CompareSamplesDefault].
//
// Collect owns the measurement loop so that it can control the things that
// systematically bias a comparison and that are easy to get wrong by hand:
// measurement order, warm-up, garbage collection placement, and the division by
// the inner loop count. It deliberately does not own the inner loop, see [Batch].
//
// A note on attenuation, which the returned numbers cannot express: every batch
// includes whatever fixed per-operation overhead the batch body carries (loop
// counter, accumulator, call frames, any input regeneration the candidate needs).
// That overhead is present in both candidates and therefore never flips the sign
// of a comparison, but it does shrink its magnitude. In a controlled experiment
// where the true difference was exactly 50%, the measured difference was 35%,
// because a fixed 1.81 ns/op of loop overhead sat on top of 2.13 ns/op of real
// work. Subtracting an empty-loop baseline does not repair this, because the
// compiler optimizes an empty loop differently than a real one; that correction
// recovered 2 of the 15 missing percentage points. Use [Candidate.Setup]
// for everything that can be hoisted out of the loop, and read the result as the
// speedup of the measured region as a whole, not of the isolated function.
//
// A note on the noise floor, which choosing an [Order] does not remove: across
// many A/A runs of identical candidates, the observed difference between the two
// sample sets has reached anywhere from a few tenths of a percent to well over
// one, under every order strategy. Differences of
// that size are indistinguishable from machine noise in a single run no matter
// how many bootstrap resamples are spent on them, because the bootstrap only
// quantifies the spread of the samples it was given and cannot see a bias that
// affected all of them. Treat a result below roughly 1% as "not resolved" rather
// than as a small but real effect.
//
// A note on what ran before: the warm-up alternates the candidates for at least
// CollectOptions.WarmupDuration so that neither starts the measurement with the
// caches to itself, whichever of them the caller built, validated or calibrated
// last. See [DefaultWarmupDuration] for why a count of batches is not enough,
// and [ValidatePair] for validating both candidates without handing one of them
// that head start in the first place.
//
// Collect returns an error if either candidate has a nil Batch, if Repeats,
// Warmup or WarmupDuration is negative, if Repeats is below
// [MinimumDataPoints], if Order is not one of the defined constants, or if
// automatic calibration of InnerLoops fails. It does not otherwise inspect the
// collected samples.
func Collect(a, b Candidate, opt CollectOptions) (samplesA, samplesB []float64, err error) {
	if a.Batch == nil {
		return nil, nil, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", a.label("A"))
	}
	if b.Batch == nil {
		return nil, nil, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", b.label("B"))
	}
	s, err := opt.schedule()
	if err != nil {
		return nil, nil, err
	}

	if opt.DisableGC {
		previous := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(previous)
	}

	if s.innerLoops == 0 {
		// DisableGC is already in effect for the whole call, so it is not
		// repeated here; the rest of the conditions must match the real run.
		s.innerLoops, err = calibratePair(a, b, CalibrationOptions{
			MaxQuantizationError: opt.MaxQuantizationError,
			MaxInnerLoops:        opt.MaxInnerLoops,
			GCBetween:            opt.GCBetween,
		})
		if err != nil {
			return nil, nil, err
		}
	}

	samples := measureInTurn([]Candidate{a, b}, s)
	return samples[0], samples[1], nil
}

// schedule is a CollectOptions with every default resolved and every check
// passed, so that the loops that run it have nothing left to decide. It exists
// because [Collect] and the A/A validations measure different sets of
// candidates under the same options and must not interpret them differently.
type schedule struct {
	repeats        int
	innerLoops     uint64 // zero until calibrated
	order          Order
	gc             bool
	disableGC      bool
	warmupRounds   int
	warmupDuration time.Duration
	seed           uint64
}

// schedule checks the options and resolves their defaults. InnerLoops is
// passed through as it is, zero included, because calibrating it needs the
// candidates.
func (opt CollectOptions) schedule() (schedule, error) {
	if opt.Repeats < 0 {
		return schedule{}, fmt.Errorf("rtcompare: Repeats must not be negative, got %d", opt.Repeats)
	}
	if opt.Repeats == 0 {
		opt.Repeats = DefaultRepeats
	}
	if uint64(opt.Repeats) < MinimumDataPoints {
		return schedule{}, fmt.Errorf("rtcompare: Repeats must be at least %d, got %d", MinimumDataPoints, opt.Repeats)
	}
	if opt.Warmup < 0 {
		return schedule{}, fmt.Errorf("rtcompare: Warmup must not be negative, got %d; use SkipWarmup to disable warm-up", opt.Warmup)
	}
	if opt.WarmupDuration < 0 {
		return schedule{}, fmt.Errorf("rtcompare: WarmupDuration must not be negative, got %v; use SkipWarmup to disable warm-up", opt.WarmupDuration)
	}
	switch opt.Order {
	case OrderABBA, OrderRandom, OrderSequential:
	default:
		return schedule{}, fmt.Errorf("rtcompare: unknown Order %d", int(opt.Order))
	}

	s := schedule{
		repeats:        opt.Repeats,
		innerLoops:     opt.InnerLoops,
		order:          opt.Order,
		gc:             opt.GCBetween,
		disableGC:      opt.DisableGC,
		warmupRounds:   opt.Warmup,
		warmupDuration: opt.WarmupDuration,
		seed:           opt.Seed,
	}
	if s.warmupRounds == 0 {
		s.warmupRounds = DefaultWarmup
	}
	if s.warmupDuration == 0 {
		s.warmupDuration = DefaultWarmupDuration
	}
	if opt.SkipWarmup {
		s.warmupRounds, s.warmupDuration = 0, 0
	}
	return s, nil
}

// calibratePair sizes the batches for two candidates and returns the larger of
// the two sizes. The cheaper candidate needs the larger batch, so the maximum
// leaves both at or above the target duration while keeping the operation count
// identical for both.
func calibratePair(a, b Candidate, opt CalibrationOptions) (uint64, error) {
	calA, err := CalibrateInnerLoops(a, opt)
	if err != nil {
		return 0, fmt.Errorf("calibrating candidate %s: %w", a.label("A"), err)
	}
	calB, err := CalibrateInnerLoops(b, opt)
	if err != nil {
		return 0, fmt.Errorf("calibrating candidate %s: %w", b.label("B"), err)
	}
	return max(calA.InnerLoops, calB.InnerLoops), nil
}

// measureInTurn warms the candidates up and then measures them in rounds, each
// candidate once per round, and returns one sample series per candidate.
//
// It is the loop behind [Collect], generalised from two candidates to any
// number so that the A/A validations of two candidates can be interleaved
// rather than run one after the other; see [ValidatePair]. Each round goes
// through the candidates forwards or backwards, as the order decides. For two
// candidates that is exactly ABBA, Random or Sequential. For more it keeps the
// property that matters: under ABBA every candidate holds every position equally
// often, and none of them runs alone for longer than two batches.
func measureInTurn(cands []Candidate, s schedule) [][]float64 {
	if s.disableGC {
		previous := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(previous)
	}

	warmUp(cands, s)

	var rng DPRNG
	if s.order == OrderRandom {
		if s.seed == 0 {
			rng = NewDPRNG()
		} else {
			rng = NewDPRNG(s.seed)
		}
	}

	samples := make([][]float64, len(cands))
	for i := range samples {
		samples[i] = make([]float64, 0, s.repeats)
	}
	for round := range s.repeats {
		forward := true
		switch s.order {
		case OrderABBA:
			forward = round%2 == 0
		case OrderRandom:
			// Uint32N uses the high bits of the scrambled state, which mix
			// better than the low bit would.
			forward = rng.Uint32N(2) == 0
		case OrderSequential:
			forward = true
		}
		for k := range cands {
			j := turn(k, len(cands), forward)
			samples[j] = append(samples[j], runBatch(cands[j], s.innerLoops, s.gc))
		}
	}
	return samples
}

// warmUp runs unmeasured rounds until both the round count and the duration of
// the schedule have been reached. It reverses the order every round, like
// OrderABBA, because a warm-up that always ends on the same candidate hands that
// candidate the caches.
func warmUp(cands []Candidate, s schedule) {
	start := time.Now()
	for round := 0; round < s.warmupRounds || time.Since(start) < s.warmupDuration; round++ {
		for k := range cands {
			runBatch(cands[turn(k, len(cands), round%2 == 0)], s.innerLoops, s.gc)
		}
	}
}

// turn returns the index of the k-th candidate to run in a round of n, going
// forwards or backwards.
func turn(k, n int, forward bool) int {
	if forward {
		return k
	}
	return n - 1 - k
}

// timeBatch runs one candidate's lifecycle for a single batch of n operations
// and returns the measured duration of the batch in nanoseconds.
//
// The order is Setup, optional collection, measure, Teardown. The collection is
// placed after Setup so that garbage produced by Setup is gone before the clock
// starts, and the clock is read as tightly around the batch call as possible.
//
// It is marked noinline so that the call sequence around the measured region is
// identical for both candidates regardless of how its callers are compiled. The
// cost of that is one non-inlined call per batch, which is amortized over n
// operations.
//
//go:noinline
func timeBatch(c Candidate, n uint64, gc bool) int64 {
	if c.Setup != nil {
		c.Setup()
	}
	if gc {
		runtime.GC()
	}
	t1 := SampleTime()
	c.Batch(n)
	t2 := SampleTime()
	elapsed := DiffTimeStamps(t1, t2)
	if c.Teardown != nil {
		c.Teardown()
	}
	return elapsed
}

// runBatch times a single batch and reduces it to nanoseconds per operation.
func runBatch(c Candidate, n uint64, gc bool) float64 {
	return float64(timeBatch(c, n, gc)) / float64(n)
}

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
