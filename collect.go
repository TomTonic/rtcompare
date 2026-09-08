package rtcompare

import (
	"fmt"
	"runtime"
	"runtime/debug"
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
// Measurement order matters. Machines drift over the course of a measurement
// run (thermal throttling, frequency scaling, page cache warm-up), and a fixed
// A-then-B order converts that drift into a systematic bias in favour of one
// candidate.
//
// The three strategies were calibrated against each other by running 25
// independent A/A experiments each, that is, the same candidate measured as both
// A and B. An unbiased harness must report a mean confidence of 0.5 for "A is
// faster than B":
//
//	Order        mean P   median P   P > 0.95
//	Sequential    0.608      0.675         0%
//	ABBA          0.473      0.507         0%
//	Random        0.526      0.476         8%
//
// Deciding the order costs nothing: the decision is made before the clock is
// read, outside the measured region.
type Order int

const (
	// OrderABBA alternates the order every repeat: A then B on even repeats,
	// B then A on odd repeats. Over each block of four batches both candidates
	// occupy the same mean position in the run and are each preceded by the
	// other exactly half the time, which cancels first-order drift as well as
	// carry-over effects between neighbouring batches. It measured best in the
	// A/A calibration above and is the zero value, so the default is the safe
	// choice.
	OrderABBA Order = iota

	// OrderRandom picks the order independently at random for each repeat,
	// driven by CollectOptions.Seed for reproducibility. It is unbiased on
	// average but showed noticeably heavier tails than OrderABBA in the A/A
	// calibration, that is, a higher chance of producing a spuriously confident
	// result from pure noise. Use it when the interference you are worried about
	// might itself be periodic and could alias with the strict alternation of
	// ABBA; prefer OrderABBA otherwise.
	OrderRandom

	// OrderSequential always measures A before B. This reproduces the naive
	// methodology and exists for comparison and regression testing. It is
	// measurably biased, see the A/A calibration above, and should not be used
	// to produce results.
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
// when CollectOptions.Repeats is left at zero. It is odd so that the median of
// the samples is an observed value rather than an interpolation, and it is
// comfortably above [MinimumDataPoints] so that the bootstrap has enough
// distinct values to resample from.
const DefaultRepeats = 101

// DefaultWarmup is the number of unmeasured batches [Collect] runs per candidate
// before collecting samples, when CollectOptions.Warmup is left at zero.
const DefaultWarmup = 1

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
	// this way; a per-operation difference of 1.89 ns was measured to 0.06
	// percentage points of accuracy against a 41 ns clock floor.
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
	MaxQuantizationError float64

	// MaxInnerLoops caps the batch size automatic calibration will try. Zero
	// selects [DefaultMaxInnerLoops]. Ignored when InnerLoops is set explicitly.
	MaxInnerLoops uint64

	// Order selects the measurement order within each repeat. The zero value
	// is [OrderABBA].
	Order Order

	// Warmup is the number of unmeasured batches run per candidate before sample
	// collection starts, to fault in pages, grow stacks and train branch
	// predictors and caches. Zero selects [DefaultWarmup]. To run no warm-up at
	// all, set SkipWarmup rather than passing a negative number.
	Warmup int

	// SkipWarmup disables warm-up entirely. This exists as its own field so that
	// Warmup keeps a single unambiguous meaning; "no warm-up" is a mode, not a
	// count. Measuring without warm-up means the first samples include one-time
	// costs such as page faults and stack growth.
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
// where the true difference was exactly 50%, the measured difference was 34%
// because a fixed 1.81 ns/op of loop overhead sat on top of 2.13 ns/op of real
// work. Subtracting an empty-loop baseline does not repair this, because the
// compiler optimizes an empty loop differently than a real one; the correction
// recovered only 2 of the 16 missing percentage points. Use [Candidate.Setup]
// for everything that can be hoisted out of the loop, and read the result as the
// speedup of the measured region as a whole, not of the isolated function.
//
// A note on the noise floor, which choosing an [Order] does not remove: across
// 25 A/A runs of an identical candidate, the observed difference between the two
// sample sets reached 0.6% to 0.8% under every order strategy. Differences of
// that size are indistinguishable from machine noise in a single run no matter
// how many bootstrap resamples are spent on them, because the bootstrap only
// quantifies the spread of the samples it was given and cannot see a bias that
// affected all of them. Treat a result below roughly 1% as "not resolved" rather
// than as a small but real effect.
//
// Collect returns an error if either candidate has a nil Batch, if Repeats or
// Warmup is negative, if Repeats is below [MinimumDataPoints], if Order is not
// one of the defined constants, or if automatic calibration of InnerLoops fails.
// It does not otherwise inspect the collected samples.
func Collect(a, b Candidate, opt CollectOptions) (samplesA, samplesB []float64, err error) {
	if a.Batch == nil {
		return nil, nil, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", a.label("A"))
	}
	if b.Batch == nil {
		return nil, nil, fmt.Errorf("rtcompare: candidate %s has a nil Batch function", b.label("B"))
	}
	if opt.Repeats < 0 {
		return nil, nil, fmt.Errorf("rtcompare: Repeats must not be negative, got %d", opt.Repeats)
	}
	if opt.Repeats == 0 {
		opt.Repeats = DefaultRepeats
	}
	if uint64(opt.Repeats) < MinimumDataPoints {
		return nil, nil, fmt.Errorf("rtcompare: Repeats must be at least %d, got %d", MinimumDataPoints, opt.Repeats)
	}
	if opt.Warmup < 0 {
		return nil, nil, fmt.Errorf("rtcompare: Warmup must not be negative, got %d; use SkipWarmup to disable warm-up", opt.Warmup)
	}
	switch opt.Order {
	case OrderABBA, OrderRandom, OrderSequential:
	default:
		return nil, nil, fmt.Errorf("rtcompare: unknown Order %d", int(opt.Order))
	}

	warmup := opt.Warmup
	if warmup == 0 {
		warmup = DefaultWarmup
	}
	if opt.SkipWarmup {
		warmup = 0
	}

	if opt.DisableGC {
		previous := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(previous)
	}

	if opt.InnerLoops == 0 {
		// DisableGC is already in effect for the whole call, so it is not
		// repeated here; the rest of the conditions must match the real run.
		calOpt := CalibrationOptions{
			MaxQuantizationError: opt.MaxQuantizationError,
			MaxInnerLoops:        opt.MaxInnerLoops,
			GCBetween:            opt.GCBetween,
		}
		calA, err := CalibrateInnerLoops(a, calOpt)
		if err != nil {
			return nil, nil, fmt.Errorf("calibrating candidate %s: %w", a.label("A"), err)
		}
		calB, err := CalibrateInnerLoops(b, calOpt)
		if err != nil {
			return nil, nil, fmt.Errorf("calibrating candidate %s: %w", b.label("B"), err)
		}
		// The cheaper candidate needs the larger batch. Using the maximum for
		// both keeps the operation count identical and leaves both batches at
		// or above the target duration.
		opt.InnerLoops = max(calA.InnerLoops, calB.InnerLoops)
	}

	for range warmup {
		runBatch(a, opt.InnerLoops, opt.GCBetween)
		runBatch(b, opt.InnerLoops, opt.GCBetween)
	}

	var rng DPRNG
	if opt.Order == OrderRandom {
		if opt.Seed == 0 {
			rng = NewDPRNG()
		} else {
			rng = NewDPRNG(opt.Seed)
		}
	}

	samplesA = make([]float64, 0, opt.Repeats)
	samplesB = make([]float64, 0, opt.Repeats)

	for i := range opt.Repeats {
		aFirst := true
		switch opt.Order {
		case OrderABBA:
			aFirst = i%2 == 0
		case OrderRandom:
			// UInt32N uses the high bits of the scrambled state, which mix
			// better than the low bit would.
			aFirst = rng.UInt32N(2) == 0
		case OrderSequential:
			aFirst = true
		}

		if aFirst {
			samplesA = append(samplesA, runBatch(a, opt.InnerLoops, opt.GCBetween))
			samplesB = append(samplesB, runBatch(b, opt.InnerLoops, opt.GCBetween))
		} else {
			samplesB = append(samplesB, runBatch(b, opt.InnerLoops, opt.GCBetween))
			samplesA = append(samplesA, runBatch(a, opt.InnerLoops, opt.GCBetween))
		}
	}

	return samplesA, samplesB, nil
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
