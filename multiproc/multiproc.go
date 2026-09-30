// Package multiproc runs rtcompare comparisons in several processes and pools
// their results, so that the reported interval covers the scatter between
// processes and not only the noise within one.
//
// A single process fixes its memory layout for its whole lifetime, and for data
// larger than the caches or full of pointers that layout can move a measured
// difference by several points. The interval of a single [rtcompare.Report]
// cannot see this: the next process reports a different, equally narrow
// interval somewhere else (issue #109). The remedy is to treat one process as
// one observation.
//
// Everything that takes is done here, so that none of it has to be remembered:
// the program is started again as child processes; each child
// perturbs its heap from its own seed before anything is built; the two
// candidates' data is built in an order that alternates between processes,
// since whichever is built second can be consistently a few percent faster;
// the reports are pooled per comparison with [rtcompare.Combine]; and
// and the first processes decide how many more every pooled interval needs to
// be precise enough.
//
// A program needs nothing but the comparisons:
//
//	func main() {
//		multiproc.Main(multiproc.Options{}, multiproc.Pair{
//			Name: "lookup",
//			A:    func() rtcompare.Candidate { return lookupIn(buildTreeA()) },
//			B:    func() rtcompare.Candidate { return lookupIn(buildTreeB()) },
//		})
//	}
//
// In a test, [RunTest] does the same and returns the results for assertions.
// [MainSuite] and [RunTestSuite] take a suite instead of pairs, such as the
// workload package's Suite for comparing data structures, and [Suites]
// combines several suites into one. [Run] is the general form underneath.
//
// # Two regimes
//
// By default the children run one after another, each with the machine to
// itself. That is the serial regime, and it answers how the candidates compare
// on an otherwise idle machine. Options.Parallel runs several children at the
// same time instead, in waves. That is a different regime, not just a faster
// one: the children share the last-level cache, the memory bandwidth and the
// clock headroom, much as a program shares them with its neighbours in
// production. Where the data is out of cache, the parallel regime is usually
// the only way to precision in reasonable time, because the variance of the
// pooled estimate is roughly
//
//	σ²_between/P + σ²_within/(P·R)
//
// for P processes of R samples, and out of cache the first term dominates by
// far: precision improves almost only with P, and running children at the
// same time multiplies P per hour by the number of parallel slots. Where the
// question is how a structure performs with the whole cache to itself, or
// where a result must stand next to earlier serial ones, stay serial.
//
// The two regimes' results are not interchangeable: all-core load lowers the
// clock and shrinks each process's share of the cache, which moves the point
// where accesses go to memory to smaller data. Never pool or compare them with
// each other. Results.Parallel records which one ran, and reports should state
// it. Within a process, A and B still run interleaved in both regimes, so
// neighbouring processes widen the noise but do not favour either candidate.
package multiproc

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/TomTonic/rtcompare"
)

// The environment variables through which a parent tells a child that it is
// one, and what it is to do.
const (
	envOut   = "RTCOMPARE_MULTIPROC_OUT"
	envSeed  = "RTCOMPARE_MULTIPROC_SEED"
	envIndex = "RTCOMPARE_MULTIPROC_INDEX"
)

// Defaults for [Options], taken from downstream suites that pooled hundreds
// of comparisons this way.
//
// Five processes were enough while the data fit in the caches. Out of cache
// the first suite needed 13 to 15 before every interval was within two
// points, which set the old serial budget of 20; later runs needed 20 to 40,
// and where processes scattered by 9 to 12 points the precision asked for
// would have needed 70 to 130 (issue #115). DefaultMaxProcesses of 40 keeps a serial run
// bounded while covering a scatter of about 6 points. A parallel run is
// budgeted in waves instead: DefaultMaxWaves of 10 costs about the wall time
// of 10 serial processes, since a wave takes about as long as one process,
// and at Parallel 12 allows 120 processes, enough for a scatter of 10 points
// and more.
const (
	DefaultMinProcesses = 5
	DefaultMaxProcesses = 40
	DefaultMaxWaves     = 10
	DefaultRotation     = 2
	DefaultAbsPrecision = 0.02
	DefaultRelPrecision = 0.10
)

// Options configures [Run]. The zero value is usable and selects the
// documented defaults.
type Options struct {
	// MinProcesses is the size of the run's first stage: the processes whose
	// scatter decides how many processes the whole run needs, once, see
	// AbsPrecision. Zero selects [DefaultMinProcesses]. Must be at least 3, the
	// minimum [rtcompare.Combine] pools. It is rounded up to a whole Rotation,
	// and in a parallel run to a whole wave. A larger first stage estimates the
	// scatter better and so asks for fewer extra processes, at the price of
	// never running fewer.
	MinProcesses int

	// MaxProcesses bounds the number of processes. Zero selects
	// [DefaultMaxProcesses] for a serial run and [DefaultMaxWaves] waves for a
	// parallel one. In a parallel run an explicit value is rounded up to a
	// whole wave, and so is MinProcesses, since a wave runs in full anyway.
	// Must not be below MinProcesses.
	MaxProcesses int

	// Parallel is the number of child processes that run at the same time.
	// Zero or 1 selects the serial regime, one process after another. Above 1,
	// processes are started in waves of Parallel, rounded up to a whole
	// Rotation so that every build order is represented equally in each wave,
	// and the first stage and the whole run are whole waves.
	//
	// Parallel runs measure a loaded machine: the children share caches,
	// memory bandwidth and clock headroom, much as a program in production
	// shares them with its neighbours. That is a legitimate regime, and for
	// data out of cache usually the faster way to precision, but it is a
	// different one: all-core load lowers the clock and shrinks each process's
	// share of the last-level cache, so its results must not be pooled or
	// compared with a serial run's; Results.Parallel records which one ran.
	// Keep it at or below the number of physical cores. Two children on the
	// SMT siblings of one core share its L1 and L2 caches and disturb each
	// other far more than neighbours on other cores do.
	//
	// Unless the environment already sets GOMAXPROCS, each child of a parallel
	// run gets GOMAXPROCS = max(2, NumCPU/Parallel), so that one child's
	// garbage collector cannot take cores from its neighbours in the middle of
	// their measurements.
	Parallel int

	// Rotation is the number of build orders the suite cycles through by
	// Process.Index, such as 2 for building A's data last in even processes and
	// B's in odd ones. The first stage and the whole run are rounded up to a
	// whole rotation, so that every order is represented equally in the pooled
	// result. Zero selects [DefaultRotation], which matches that two-way
	// alternation and costs a suite that does not alternate at most one extra
	// process; 1 rounds nothing.
	Rotation int

	// AbsPrecision and RelPrecision are the precision asked for: every
	// comparison's pooled interval should have a half-width of at most
	// AbsPrecision, or of at most RelPrecision times its |Delta|. After the
	// first stage of MinProcesses, the run computes once how many processes
	// that takes, from the scatter the first stage showed, and then runs that
	// many, up to MaxProcesses, without looking at the intervals again; see
	// [rtcompare.Pooled.ProcessesFor] for why. Zero selects
	// [DefaultAbsPrecision] and [DefaultRelPrecision].
	AbsPrecision, RelPrecision float64

	// Level is the coverage level of the pooled intervals. Zero selects
	// [rtcompare.DefaultConfidenceLevel].
	Level float64

	// Seed determines the per-process seeds, so that a whole run can be
	// repeated. Zero selects a random one; Results.Seeds records what each
	// process got either way.
	Seed uint64

	// Executable is the program to start as a child. Empty selects the running
	// binary, from os.Executable.
	Executable string

	// Args are the arguments for the children. Nil selects os.Args[1:], which
	// repeats the parent's own command line. In a test, run only the calling
	// test in the children, or every test of the package runs in every child:
	//
	//	Args: []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"}
	Args []string

	// Stdout and Stderr receive the children's output. Nil discards stdout and
	// passes stderr through to the parent's. In a parallel run the children's
	// output is written in whole lines, each prefixed with its process index
	// such as "[p07] ", so that concurrent children do not interleave within
	// a line.
	Stdout, Stderr io.Writer

	// Progress, when set, is called with the results so far after each
	// process of a serial run and after each complete wave of a parallel one,
	// for example to print a line per call.
	Progress func(Results)
}

// Process is what a child knows about itself. The suite receives it and
// records its results through it.
type Process struct {
	// Index counts the processes of a run from zero.
	Index int

	// Seed is this process's seed. The heap perturbation Run applies before
	// the suite and Rand are derived from it, so a process can be repeated by
	// running the suite again with the same seed.
	Seed uint64

	records []record
}

// Record hands a comparison's report to the parent under a name. The same name
// in every process identifies the same comparison; recording a name twice in
// one process records two observations of it.
func (p *Process) Record(name string, r rtcompare.Report) {
	p.records = append(p.records, newRecord(name, r))
}

// Rand returns a generator seeded from this process's seed, independent of the
// heap perturbation, for shuffling the order in which many fixtures are built;
// see [rtcompare.DPRNG.Shuffle]. For two fixtures, alternate by Index instead,
// which balances exactly where a random draw over a few processes rarely does.
func (p *Process) Rand() *rtcompare.DPRNG {
	rng := rtcompare.NewDPRNG((p.Seed ^ 0x5DEECE66D) | 1)
	return &rng
}

// Comparison collects one named comparison across processes.
type Comparison struct {
	// Name is the name the suite recorded it under.
	Name string

	// Reports holds one report per process that recorded it, in process order.
	// They carry the estimate, confidences, noise floor, verdict, suspension
	// and warnings of each process, and of the validations only the A/A
	// differences in
	// ValidationA.Deltas and ValidationB.Deltas; the raw samples and the rest
	// of the validations stay in the child.
	Reports []rtcompare.Report

	// Pooled is the result of pooling Reports: [rtcompare.CombineStaged] once
	// the run's first stage is complete, [rtcompare.Combine] before. It is the
	// zero value until three processes have recorded the comparison.
	Pooled rtcompare.Pooled

	// firstStage is how many reports the first stage held, zero until then.
	firstStage int
}

// Results is what [Run] found.
type Results struct {
	// Child is true in a child process, where the other fields are empty. The
	// caller should return without reporting anything.
	Child bool

	// Processes is how many processes ran, and Seeds the seed each one got.
	Processes int
	Seeds     []uint64

	// Comparisons holds every named comparison in the order it was first
	// recorded.
	Comparisons []Comparison

	// Precise reports whether the run reached the number of processes its
	// first stage called for; false means that number exceeded MaxProcesses,
	// so the run stopped there with at least one interval wider than
	// requested.
	Precise bool

	// Parallel is the number of processes that ran at the same time, 1 for a
	// serial run, so that a report can say which regime it measured. Results
	// from the two regimes are not interchangeable; see Options.Parallel.
	Parallel int

	// ChildGOMAXPROCS is the GOMAXPROCS the children's environment set, zero
	// when it set none and the children used the Go runtime's default.
	ChildGOMAXPROCS int
}

// String renders the pooled results, one block per comparison.
func (r Results) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d processes", r.Processes)
	if r.Parallel > 1 {
		fmt.Fprintf(&b, ", %d at a time (GOMAXPROCS %d each)", r.Parallel, r.ChildGOMAXPROCS)
	}
	if !r.Precise {
		b.WriteString(", stopped before every interval was as precise as requested")
	}
	b.WriteString("\n")
	for _, c := range r.Comparisons {
		fmt.Fprintf(&b, "\n%s:\n", c.Name)
		if c.Pooled.Processes == 0 {
			fmt.Fprintf(&b, "  recorded by %d processes, too few to pool\n", len(c.Reports))
			continue
		}
		for _, line := range strings.Split(c.Pooled.String(), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Run runs suite in several child processes and pools what they record.
//
// Parameters: opt sets how many processes to run and when to stop, see
// [Options]; suite is the measurement one process performs. It receives a
// [Process] with that process's seed, and records each comparison's report
// under a name with Process.Record.
//
// Run behaves differently depending on where it is called. In the parent, the
// program as started by the user, it never calls suite: it starts the current
// binary again as a child, with the same arguments unless Options.Args says
// otherwise, waits for it, and repeats: one process at a time, or in waves of
// Options.Parallel processes at a time (see the package documentation for the
// two regimes). Process i always gets index i and the same seed, whichever
// child finishes first, and results are pooled in index order, so a run is
// repeatable from its seed in either regime. The first MinProcesses are the
// first stage: from their scatter, Run decides once how many processes every
// comparison's interval needs to meet the precision in Options, rounded up to
// a whole Rotation or wave and at most MaxProcesses, and then runs exactly
// that many; see Options.AbsPrecision. If a child of a wave fails, its
// siblings are killed and the error of the lowest failing index is returned.
// It returns the pooled results. In a child, Run perturbs the heap from the process's seed (see
// [rtcompare.PerturbHeap]), calls suite once, hands its records to the parent
// through a file, and returns Results with Child set; the caller should then
// return without doing anything else, since the parent does the reporting.
// [Main] and [RunTest] take care of that. The children find out which they are
// from environment variables that Run sets for them.
//
// Use it for any comparison whose data is larger than the caches or full of
// pointers, where a single process's interval is known to be several times too
// narrow; see [rtcompare.Combine] for the numbers. It costs a process start and
// the whole suite per process, and out of cache that has taken from 13 to more
// than 40 processes; see [DefaultMaxProcesses] and Options.Parallel. Keep the machine awake while it runs: each Report notices a
// suspension, but the time is lost.
//
// An error is returned for invalid options, if a child cannot be started,
// exits with a failure, returns an error from suite or records nothing, and if
// pooling fails. In a child, the error is suite's.
func Run(opt Options, suite func(*Process) error) (Results, error) {
	if path := os.Getenv(envOut); path != "" {
		return Results{Child: true}, runChild(path, suite)
	}
	opt, err := opt.resolve()
	if err != nil {
		return Results{}, err
	}
	dir, err := os.MkdirTemp("", "rtcompare-multiproc-*")
	if err != nil {
		return Results{}, fmt.Errorf("multiproc: creating a directory for the children's results: %w", err)
	}
	// A leftover directory of small result files in the system's temporary
	// directory is not worth failing a finished run for.
	defer func() { _ = os.RemoveAll(dir) }()

	w := newWaveRunner(opt, dir)
	res := Results{Parallel: opt.Parallel, ChildGOMAXPROCS: w.gomaxprocs}
	index := map[string]int{}
	// The run's size is decided once, from its first stage, and not by looking
	// at the interval after every process; see planSize.
	target := opt.MaxProcesses
	for start := 0; start < target; start += opt.Parallel {
		recs, err := w.run(start, min(opt.Parallel, target-start))
		if err != nil {
			return res, err
		}
		// In index order, whichever child finished first, so that a run is
		// repeatable from its seed.
		for k, wave := range recs {
			res.Processes++
			res.Seeds = append(res.Seeds, processSeed(opt.Seed, start+k))
			for _, rec := range wave {
				j, ok := index[rec.Name]
				if !ok {
					j = len(res.Comparisons)
					index[rec.Name] = j
					res.Comparisons = append(res.Comparisons, Comparison{Name: rec.Name})
				}
				res.Comparisons[j].Reports = append(res.Comparisons[j].Reports, rec.report())
			}
		}
		if res.Processes == opt.MinProcesses {
			target, res.Precise = res.planSize(opt)
		}
		if err := res.pool(opt.Level); err != nil {
			return res, err
		}
		if opt.Progress != nil {
			opt.Progress(res)
		}
	}
	return res, nil
}

// planSize decides, once the first stage of MinProcesses has run, how many
// processes the whole run needs, and whether that is within MaxProcesses.
//
// It is Stein's two-stage procedure: each comparison's first-stage scatter
// gives the number of processes its interval needs (see
// rtcompare.Pooled.ProcessesFor), the largest of them is rounded up to a whole
// Rotation or wave and capped at MaxProcesses, and the run then goes that far
// without looking at the intervals again. Checking the interval after every
// process instead, and stopping as soon as it was narrow enough, stopped
// preferentially where the scatter had come out low, and its 95% intervals
// covered 92 to 94% (issue #119). pool then reports Stein's interval, which
// covers at its level.
func (r *Results) planSize(opt Options) (target int, precise bool) {
	need := r.Processes
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		if len(c.Reports) < 3 {
			return opt.MaxProcesses, false
		}
		p, err := rtcompare.Combine(c.Reports, opt.Level)
		if err != nil {
			// pool reports the error.
			return opt.MaxProcesses, false
		}
		c.firstStage = len(c.Reports)
		need = max(need, p.ProcessesFor(opt.AbsPrecision, opt.RelPrecision))
	}
	if len(r.Comparisons) == 0 || need > opt.MaxProcesses {
		return opt.MaxProcesses, false
	}
	return min(opt.MaxProcesses, roundUp(need, max(opt.Parallel, opt.Rotation))), true
}

// processSeed is the seed of process i: the same for a given run seed however
// many processes run at a time.
func processSeed(runSeed uint64, i int) uint64 {
	return splitmix(runSeed + uint64(i))
}

// resolve checks the options and fills in their defaults.
func (opt Options) resolve() (Options, error) {
	if opt.Parallel < 0 {
		return opt, fmt.Errorf("multiproc: Parallel must not be negative, got %d", opt.Parallel)
	}
	if opt.Rotation == 0 {
		opt.Rotation = DefaultRotation
	}
	if opt.Rotation < 0 {
		return opt, fmt.Errorf("multiproc: Rotation must not be negative, got %d", opt.Rotation)
	}
	if err := opt.resolveCounts(); err != nil {
		return opt, err
	}
	if opt.AbsPrecision == 0 {
		opt.AbsPrecision = DefaultAbsPrecision
	}
	if opt.RelPrecision == 0 {
		opt.RelPrecision = DefaultRelPrecision
	}
	if !(opt.AbsPrecision > 0) || !(opt.RelPrecision > 0) {
		return opt, fmt.Errorf("multiproc: AbsPrecision and RelPrecision must be positive, got %v and %v", opt.AbsPrecision, opt.RelPrecision)
	}
	if opt.Seed == 0 {
		var b [8]byte
		if _, err := rand.Read(b[:]); err != nil {
			return opt, fmt.Errorf("multiproc: drawing a seed: %w", err)
		}
		opt.Seed = binary.LittleEndian.Uint64(b[:])
	}
	if opt.Executable == "" {
		exe, err := os.Executable()
		if err != nil {
			return opt, fmt.Errorf("multiproc: finding the running binary to start as a child: %w", err)
		}
		opt.Executable = exe
	}
	if opt.Args == nil {
		opt.Args = os.Args[1:]
	}
	if opt.Stdout == nil {
		opt.Stdout = io.Discard
	}
	if opt.Stderr == nil {
		opt.Stderr = os.Stderr
	}
	return opt, nil
}

// resolveCounts settles the wave size and the process budget. Afterwards
// Parallel is the wave size, 1 for a serial run, and in a parallel run both
// MinProcesses and MaxProcesses are whole waves.
func (opt *Options) resolveCounts() error {
	serial := opt.Parallel <= 1
	opt.Parallel = max(1, opt.Parallel)
	if !serial {
		opt.Parallel = roundUp(opt.Parallel, opt.Rotation)
	}
	if opt.MinProcesses == 0 {
		opt.MinProcesses = DefaultMinProcesses
	}
	if opt.MinProcesses < 3 {
		return fmt.Errorf("multiproc: MinProcesses must be at least 3, got %d", opt.MinProcesses)
	}
	if serial {
		opt.MinProcesses = roundUp(opt.MinProcesses, opt.Rotation)
		if opt.MaxProcesses == 0 {
			opt.MaxProcesses = max(DefaultMaxProcesses, opt.MinProcesses)
		}
	} else {
		opt.MinProcesses = roundUp(opt.MinProcesses, opt.Parallel)
		if opt.MaxProcesses == 0 {
			opt.MaxProcesses = max(DefaultMaxWaves*opt.Parallel, opt.MinProcesses)
		}
		opt.MaxProcesses = roundUp(opt.MaxProcesses, opt.Parallel)
	}
	if opt.MaxProcesses < opt.MinProcesses {
		return fmt.Errorf("multiproc: MaxProcesses (%d) must not be below MinProcesses (%d)", opt.MaxProcesses, opt.MinProcesses)
	}
	return nil
}

// roundUp rounds n up to a multiple of step.
func roundUp(n, step int) int {
	return (n + step - 1) / step * step
}

// pool combines every comparison that at least three processes recorded,
// with Stein's interval once its first stage is known.
func (r *Results) pool(level float64) error {
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		if len(c.Reports) < 3 {
			continue
		}
		var p rtcompare.Pooled
		var err error
		if c.firstStage > 0 {
			p, err = rtcompare.CombineStaged(c.Reports, c.firstStage, level)
		} else {
			p, err = rtcompare.Combine(c.Reports, level)
		}
		if err != nil {
			return fmt.Errorf("multiproc: pooling %q: %w", c.Name, err)
		}
		c.Pooled = p
	}
	return nil
}

// runProcess starts one child and reads back what it recorded.
func runProcess(ctx context.Context, opt Options, out string, i int, stdout, stderr io.Writer, env []string) ([]record, error) {
	seed := processSeed(opt.Seed, i)
	cmd := exec.CommandContext(ctx, opt.Executable, opt.Args...)
	cmd.Env = append(append(os.Environ(), env...),
		envOut+"="+out,
		envSeed+"="+strconv.FormatUint(seed, 10),
		envIndex+"="+strconv.Itoa(i),
	)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		suiteErr := childError(out, err)
		// Killed because a sibling failed: its own error would only hide the
		// one that caused the cancellation. A child that got as far as writing
		// a suite error failed in its own right, cancelled or not.
		if ctx.Err() != nil && suiteErr == err {
			return nil, errCanceled
		}
		return nil, fmt.Errorf("multiproc: process %d (seed %d) failed after %s: %w", i, seed, time.Since(start).Round(time.Millisecond), suiteErr)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("multiproc: process %d (seed %d) left no results; does the program call multiproc.Run? %w", i, seed, err)
	}
	var f childFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("multiproc: reading the results of process %d: %w", i, err)
	}
	if f.Error != "" {
		return nil, fmt.Errorf("multiproc: process %d (seed %d): %s", i, seed, f.Error)
	}
	if len(f.Records) == 0 {
		return nil, fmt.Errorf("multiproc: process %d (seed %d) recorded no comparisons", i, seed)
	}
	return f.Records, nil
}

// errCanceled marks a child that was killed because a sibling in its wave
// failed.
var errCanceled = errors.New("multiproc: canceled because another process of the wave failed")

// childError prefers the suite's own error, if the child got as far as writing
// it, over the bare exit status.
func childError(out string, exitErr error) error {
	data, err := os.ReadFile(out)
	if err != nil {
		return exitErr
	}
	var f childFile
	if json.Unmarshal(data, &f) != nil || f.Error == "" {
		return exitErr
	}
	return errors.New(f.Error)
}

// runChild runs the suite once as a child and writes what it recorded, or the
// error it returned, to the file the parent named.
func runChild(out string, suite func(*Process) error) error {
	seed, err := strconv.ParseUint(os.Getenv(envSeed), 10, 64)
	if err != nil {
		return fmt.Errorf("multiproc: child started without a valid %s: %w", envSeed, err)
	}
	index, err := strconv.Atoi(os.Getenv(envIndex))
	if err != nil {
		return fmt.Errorf("multiproc: child started without a valid %s: %w", envIndex, err)
	}
	p := &Process{Index: index, Seed: seed}
	// Before anything the suite builds, so that its data lands at addresses
	// that differ from one process to the next; kept alive until the suite is
	// done, so that the filler's memory is not handed to the code under test.
	spacers := rtcompare.PerturbHeap(seed)
	suiteErr := suite(p)
	spacers.KeepAlive()
	f := childFile{Records: p.records}
	if suiteErr != nil {
		f.Error = suiteErr.Error()
	}
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("multiproc: encoding the results for the parent: %w", err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		return fmt.Errorf("multiproc: writing the results for the parent: %w", err)
	}
	return suiteErr
}

// childFile is what a child hands to its parent.
type childFile struct {
	Records []record `json:"records"`
	Error   string   `json:"error,omitempty"`
}

// record is the part of a Report that crosses the process boundary; the raw
// samples are not needed for pooling and stay in the child. Of the validations, only the
// signed A/A differences cross, which rtcompare.Combine reads the systematic
// harness bias from.
type record struct {
	Name       string                `json:"name"`
	NsPerOpA   float64               `json:"ns_a"`
	NsPerOpB   float64               `json:"ns_b"`
	Estimate   rtcompare.Estimate    `json:"estimate"`
	NoiseFloor float64               `json:"noise_floor"`
	Validated  bool                  `json:"validated"`
	Resolved   bool                  `json:"resolved"`
	Suspended  time.Duration         `json:"suspended"`
	Warnings   []string              `json:"warnings,omitempty"`
	AADeltasA  []float64             `json:"aa_deltas_a,omitempty"`
	AADeltasB  []float64             `json:"aa_deltas_b,omitempty"`
	Confidence rtcompare.Confidences `json:"confidence,omitempty"`
}

func newRecord(name string, r rtcompare.Report) record {
	return record{
		Name: name, NsPerOpA: r.NsPerOpA, NsPerOpB: r.NsPerOpB, Estimate: r.Estimate,
		NoiseFloor: r.NoiseFloor, Validated: r.Validated, Resolved: r.Resolved,
		Suspended: r.Suspended, Warnings: r.Warnings,
		AADeltasA: r.ValidationA.Deltas, AADeltasB: r.ValidationB.Deltas,
		Confidence: r.Confidence,
	}
}

func (rec record) report() rtcompare.Report {
	r := rtcompare.Report{
		NsPerOpA: rec.NsPerOpA, NsPerOpB: rec.NsPerOpB, Estimate: rec.Estimate,
		NoiseFloor: rec.NoiseFloor, Validated: rec.Validated, Resolved: rec.Resolved,
		Suspended: rec.Suspended, Warnings: rec.Warnings, Confidence: rec.Confidence,
	}
	r.ValidationA.Deltas, r.ValidationB.Deltas = rec.AADeltasA, rec.AADeltasB
	return r
}

// splitmix derives well-spread seeds from consecutive integers, one round of
// splitmix64.
func splitmix(x uint64) uint64 {
	z := x + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
