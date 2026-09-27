// Package multiproc runs rtcompare comparisons in several processes and pools
// their results, so that the reported interval covers the scatter between
// processes and not only the noise within one.
//
// A single process fixes its memory layout for its whole lifetime, and for data
// larger than the caches or full of pointers that layout can move a measured
// difference by several points. The interval of a single [rtcompare.Report]
// cannot see this: the next process reports a different, equally narrow
// interval somewhere else (issue #109). The remedy is to treat one process as
// one observation. [Run] re-executes the current binary as child processes,
// gives each child its own seed for perturbing its heap and an index for
// alternating the order in which it builds its data, collects the reports each
// child records, and pools them per comparison with [rtcompare.Combine]. It
// keeps starting processes until every pooled interval is precise enough,
// within bounds.
//
// Alternate the build order, don't leave it fixed. Whichever of two identical
// data structures is built second can be consistently a few percent faster, in
// every process and however the heap was perturbed; in the reproduction in
// cmd/rtcompare-aa it was 3% for two 1M-node lists. Giving each candidate the
// last position in half of the processes turns that into scatter the pooled
// interval covers. With two fixtures, alternate by Process.Index, which balances
// exactly; with many, shuffle them with Process.Rand.
//
// The whole suite runs in each child, so a program looks like this:
//
//	func main() {
//		res, err := multiproc.Run(multiproc.Options{}, func(p *multiproc.Process) error {
//			defer p.PerturbHeap().KeepAlive()
//			a, b := buildFixtures(p.Index%2 == 1) // true: build b's data first
//			rep, err := rtcompare.Compare(candidateA(a), candidateB(b), rtcompare.CompareOptions{})
//			if err != nil {
//				return err
//			}
//			p.Record("lookup", rep)
//			return nil
//		})
//		if err != nil {
//			log.Fatal(err)
//		}
//		if res.Child {
//			return // this process was one of the measured children
//		}
//		fmt.Println(res)
//	}
package multiproc

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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

// Defaults for [Options], taken from a downstream suite that pooled 46
// comparisons this way. Five processes were enough while the data fit in the
// caches; out of cache the stop rule needed 13 to 15 before every interval was
// within two points.
const (
	DefaultMinProcesses = 5
	DefaultMaxProcesses = 20
	DefaultRotation     = 2
	DefaultAbsPrecision = 0.02
	DefaultRelPrecision = 0.10
)

// Options configures [Run]. The zero value is usable and selects the
// documented defaults.
type Options struct {
	// MinProcesses is the least number of processes to run before the stop
	// rule is consulted. Zero selects [DefaultMinProcesses]. Must be at least 3,
	// the minimum [rtcompare.Combine] pools.
	MinProcesses int

	// MaxProcesses bounds the number of processes. Zero selects
	// [DefaultMaxProcesses]. Must not be below MinProcesses.
	MaxProcesses int

	// Rotation is the number of build orders the suite cycles through by
	// Process.Index, such as 2 for building A's data last in even processes and
	// B's in odd ones. The stop rule is only consulted after a whole rotation,
	// so that every order is represented equally in the pooled result. Zero
	// selects [DefaultRotation], which matches that two-way alternation and
	// costs a suite that does not alternate at most one extra process; 1
	// consults the rule after every process.
	Rotation int

	// AbsPrecision and RelPrecision are the stop rule: no more processes are
	// started once every comparison's pooled interval has a half-width of at
	// most AbsPrecision, or of at most RelPrecision times its |Delta|. Zero
	// selects [DefaultAbsPrecision] and [DefaultRelPrecision].
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
	// passes stderr through to the parent's.
	Stdout, Stderr io.Writer

	// Progress, when set, is called after each process with the results so
	// far, for example to print a line per process.
	Progress func(Results)
}

// Process is what a child knows about itself. The suite receives it and
// records its results through it.
type Process struct {
	// Index counts the processes of a run from zero.
	Index int

	// Seed is this process's seed. PerturbHeap and Rand are derived from it, so
	// a process can be repeated by running the suite again with the same seed.
	Seed uint64

	records []record
}

// Record hands a comparison's report to the parent under a name. The same name
// in every process identifies the same comparison; recording a name twice in
// one process records two observations of it.
func (p *Process) Record(name string, r rtcompare.Report) {
	p.records = append(p.records, newRecord(name, r))
}

// PerturbHeap perturbs this process's heap layout from its seed; see
// [rtcompare.PerturbHeap]. Call it first, before building any data, and keep
// the result alive until the measurements are done.
func (p *Process) PerturbHeap() *rtcompare.Spacers {
	return rtcompare.PerturbHeap(p.Seed)
}

// Rand returns a generator seeded from this process's seed, independent of the
// heap perturbation, for shuffling the order in which many fixtures are built;
// see [rtcompare.DPRNG.Shuffle]. For two fixtures, alternate by Index instead,
// which balances exactly where a random draw over a few processes rarely does.
func (p *Process) Rand() *rtcompare.DPRNG {
	rng := rtcompare.NewDPRNG(splitmix(p.Seed^0x5DEECE66D) | 1) // zero would ask NewDPRNG for a random seed
	return &rng
}

// Comparison collects one named comparison across processes.
type Comparison struct {
	// Name is the name the suite recorded it under.
	Name string

	// Reports holds one report per process that recorded it, in process order.
	// They carry the estimate, noise floor, verdict, suspension and warnings of
	// each process, but not the raw samples or validations, which stay in the
	// child.
	Reports []rtcompare.Report

	// Pooled is the result of [rtcompare.Combine] over Reports. It is the zero
	// value until three processes have recorded the comparison.
	Pooled rtcompare.Pooled
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

	// Precise reports whether the stop rule was met; false means the run
	// stopped at MaxProcesses with at least one interval still wider than
	// requested.
	Precise bool
}

// String renders the pooled results, one block per comparison.
func (r Results) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d processes", r.Processes)
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
// otherwise, waits for it, and repeats, one process at a time so that they do
// not disturb each other. Once MinProcesses have run it stops as soon as every
// comparison's pooled interval meets the precision in Options at the end of a
// whole Rotation, and in any case after MaxProcesses. It returns the pooled results. In a child, Run calls
// suite once, hands its records to the parent through a file, and returns
// Results with Child set; the caller should then return without doing anything
// else, since the parent does the reporting. The children find out which they
// are from environment variables that Run sets for them.
//
// Use it for any comparison whose data is larger than the caches or full of
// pointers, where a single process's interval is known to be several times too
// narrow; see [rtcompare.Combine] for the numbers. It costs a process start and
// the whole suite per process, and out of cache that has taken 13 to 15
// processes. Keep the machine awake while it runs: each Report notices a
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

	var res Results
	index := map[string]int{}
	for i := range opt.MaxProcesses {
		seed := splitmix(opt.Seed + uint64(i))
		recs, err := runProcess(opt, filepath.Join(dir, strconv.Itoa(i)+".json"), i, seed)
		if err != nil {
			return res, err
		}
		res.Processes++
		res.Seeds = append(res.Seeds, seed)
		for _, rec := range recs {
			j, ok := index[rec.Name]
			if !ok {
				j = len(res.Comparisons)
				index[rec.Name] = j
				res.Comparisons = append(res.Comparisons, Comparison{Name: rec.Name})
			}
			res.Comparisons[j].Reports = append(res.Comparisons[j].Reports, rec.report())
		}
		if err := res.pool(opt.Level); err != nil {
			return res, err
		}
		if opt.Progress != nil {
			opt.Progress(res)
		}
		if res.Processes >= opt.MinProcesses && res.Processes%opt.Rotation == 0 && res.precise(opt.AbsPrecision, opt.RelPrecision) {
			res.Precise = true
			break
		}
	}
	return res, nil
}

// resolve checks the options and fills in their defaults.
func (opt Options) resolve() (Options, error) {
	if opt.MinProcesses == 0 {
		opt.MinProcesses = DefaultMinProcesses
	}
	if opt.MaxProcesses == 0 {
		opt.MaxProcesses = max(DefaultMaxProcesses, opt.MinProcesses)
	}
	if opt.MinProcesses < 3 {
		return opt, fmt.Errorf("multiproc: MinProcesses must be at least 3, got %d", opt.MinProcesses)
	}
	if opt.MaxProcesses < opt.MinProcesses {
		return opt, fmt.Errorf("multiproc: MaxProcesses (%d) must not be below MinProcesses (%d)", opt.MaxProcesses, opt.MinProcesses)
	}
	if opt.Rotation == 0 {
		opt.Rotation = DefaultRotation
	}
	if opt.Rotation < 0 {
		return opt, fmt.Errorf("multiproc: Rotation must not be negative, got %d", opt.Rotation)
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

// pool combines every comparison that at least three processes recorded.
func (r *Results) pool(level float64) error {
	for i := range r.Comparisons {
		c := &r.Comparisons[i]
		if len(c.Reports) < 3 {
			continue
		}
		p, err := rtcompare.Combine(c.Reports, level)
		if err != nil {
			return fmt.Errorf("multiproc: pooling %q: %w", c.Name, err)
		}
		c.Pooled = p
	}
	return nil
}

// precise reports whether every comparison is pooled and meets the stop rule.
func (r *Results) precise(abs, rel float64) bool {
	for _, c := range r.Comparisons {
		if c.Pooled.Processes == 0 || !c.Pooled.Precise(abs, rel) {
			return false
		}
	}
	return len(r.Comparisons) > 0
}

// runProcess starts one child and reads back what it recorded.
func runProcess(opt Options, out string, i int, seed uint64) ([]record, error) {
	cmd := exec.Command(opt.Executable, opt.Args...)
	cmd.Env = append(os.Environ(),
		envOut+"="+out,
		envSeed+"="+strconv.FormatUint(seed, 10),
		envIndex+"="+strconv.Itoa(i),
	)
	cmd.Stdout, cmd.Stderr = opt.Stdout, opt.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("multiproc: process %d (seed %d) failed after %s: %w", i, seed, time.Since(start).Round(time.Millisecond), childError(out, err))
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
	suiteErr := suite(p)
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

// record is the part of a Report that crosses the process boundary. The full
// Report cannot: its Confidence map has float keys, which JSON does not allow,
// and its samples and validations are not needed for pooling.
type record struct {
	Name       string             `json:"name"`
	NsPerOpA   float64            `json:"ns_a"`
	NsPerOpB   float64            `json:"ns_b"`
	Estimate   rtcompare.Estimate `json:"estimate"`
	NoiseFloor float64            `json:"noise_floor"`
	Validated  bool               `json:"validated"`
	Resolved   bool               `json:"resolved"`
	Suspended  time.Duration      `json:"suspended"`
	Warnings   []string           `json:"warnings,omitempty"`
}

func newRecord(name string, r rtcompare.Report) record {
	return record{
		Name: name, NsPerOpA: r.NsPerOpA, NsPerOpB: r.NsPerOpB, Estimate: r.Estimate,
		NoiseFloor: r.NoiseFloor, Validated: r.Validated, Resolved: r.Resolved,
		Suspended: r.Suspended, Warnings: r.Warnings,
	}
}

func (rec record) report() rtcompare.Report {
	return rtcompare.Report{
		NsPerOpA: rec.NsPerOpA, NsPerOpB: rec.NsPerOpB, Estimate: rec.Estimate,
		NoiseFloor: rec.NoiseFloor, Validated: rec.Validated, Resolved: rec.Resolved,
		Suspended: rec.Suspended, Warnings: rec.Warnings,
	}
}

// splitmix derives well-spread seeds from consecutive integers, one round of
// splitmix64.
func splitmix(x uint64) uint64 {
	z := x + 0x9E3779B97F4A7C15
	z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9
	z = (z ^ (z >> 27)) * 0x94D049BB133111EB
	return z ^ (z >> 31)
}
