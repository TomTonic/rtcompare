package multiproc

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"testing"

	"github.com/TomTonic/rtcompare"
)

// Pair is one comparison for [Main], [RunTest] or [Pairs]: how to build each
// candidate, and the options to compare them with.
type Pair struct {
	// Name identifies the comparison in the results. It must be unique among
	// the pairs of one run.
	Name string

	// A and B build their candidate: they create the data the candidate works
	// on and return the candidate. Build everything the measurement depends
	// on here, not before, so that it is built after the heap perturbation and
	// in the alternating order. Neither may be nil.
	A, B func() rtcompare.Candidate

	// Options are passed to [rtcompare.Compare] unchanged.
	Options rtcompare.CompareOptions
}

// build calls the two builders in an order that alternates with the process
// index: A first in even processes, B first in odd ones. Whichever is built
// second lands in different memory, and has been measured consistently a few
// percent faster for it; alternating lets pooling average that out, and the
// default Options.Rotation of 2 keeps the two orders equally represented.
func (pair Pair) build(index int) (a, b rtcompare.Candidate) {
	if index%2 == 0 {
		a = pair.A()
		b = pair.B()
	} else {
		b = pair.B()
		a = pair.A()
	}
	return a, b
}

// checkPairs rejects pairs that cannot run, before any process is started.
func checkPairs(pairs []Pair) error {
	if len(pairs) == 0 {
		return fmt.Errorf("multiproc: no pairs to compare")
	}
	seen := map[string]bool{}
	for i, pair := range pairs {
		if pair.Name == "" {
			return fmt.Errorf("multiproc: pair %d has no Name", i)
		}
		if seen[pair.Name] {
			return fmt.Errorf("multiproc: two pairs are named %q", pair.Name)
		}
		seen[pair.Name] = true
		if pair.A == nil || pair.B == nil {
			return fmt.Errorf("multiproc: pair %q needs both an A and a B builder", pair.Name)
		}
	}
	return nil
}

// Pairs returns a suite for [Run] that builds and compares each pair in turn,
// and records each report under the pair's name.
//
// Parameters: pairs are the comparisons, see [Pair].
//
// Use it when Run's other options are needed with pairs, or to combine pairs
// with measurements of your own in one suite. [Main] and [RunTest] use it. The
// suite returns an error, naming the pair, if a comparison fails; it does not
// check the pairs, which Main and RunTest do before starting any process.
func Pairs(pairs ...Pair) func(*Process) error {
	return func(p *Process) error {
		for _, pair := range pairs {
			a, b := pair.build(p.Index)
			rep, err := rtcompare.Compare(a, b, pair.Options)
			if err != nil {
				return fmt.Errorf("%s: %w", pair.Name, err)
			}
			p.Record(pair.Name, rep)
		}
		return nil
	}
}

// Main runs the pairs in several processes and prints the pooled results. It
// is meant to be the whole of a benchmark program's main function.
//
// Parameters: opt configures the processes, see [Options]; pairs are the
// comparisons, see [Pair].
//
// In the parent, the process the user started, Main prints a line per finished
// process, or per finished wave in a parallel run, to standard error and the pooled results to standard output, then
// returns. In a child it runs the pairs once and exits, so that nothing after
// Main runs there. On an error it prints it to standard error and exits with
// status 1, in either role.
//
//	func main() {
//		multiproc.Main(multiproc.Options{}, multiproc.Pair{Name: "lookup", A: buildA, B: buildB})
//	}
func Main(opt Options, pairs ...Pair) {
	exitMain(runMain(opt, checkPairs(pairs), Pairs(pairs...), os.Stdout, os.Stderr))
}

// MainSuite is [Main] for a suite instead of pairs: it runs suite in several
// processes and prints the pooled results, and is meant to be the whole of a
// benchmark program's main function.
//
// Parameters: opt configures the processes, see [Options]; suite is the
// measurement one process performs, see [Run]. Suites from other packages,
// such as the workload package's Suite, fit here, and [Suites] combines
// several of them, pairs included through [Pairs].
//
// It behaves like Main in every other respect, printing progress and results
// in the parent and exiting in a child.
//
//	func main() {
//		multiproc.MainSuite(multiproc.Options{},
//			workload.Suite("map vs Set3", 1_000_000, goMap, set3Set, workload.Options{}))
//	}
func MainSuite(opt Options, suite func(*Process) error) {
	exitMain(runMain(opt, checkSuite(suite), suite, os.Stdout, os.Stderr))
}

// Suites combines several suites into one that runs them in turn, in the
// order given, and stops at the first error.
//
// Parameters: suites are the suites to run, none of them nil.
//
// Use it to measure several kinds of comparison in the same processes, for
// example pairs and a workload suite:
//
//	multiproc.MainSuite(opt, multiproc.Suites(multiproc.Pairs(lookup), workload.Suite(...)))
func Suites(suites ...func(*Process) error) func(*Process) error {
	return func(p *Process) error {
		for _, suite := range suites {
			if err := suite(p); err != nil {
				return err
			}
		}
		return nil
	}
}

// checkSuite rejects a nil suite before any process is started.
func checkSuite(suite func(*Process) error) error {
	if suite == nil {
		return fmt.Errorf("multiproc: no suite to run")
	}
	return nil
}

// exitMain ends the program as runMain decided.
func exitMain(code int, exit bool) {
	if exit {
		os.Exit(code)
	}
}

// runMain is Main without the exit, so that it can be tested. invalid is the
// result of checking what is to be run, before any process is started. It
// reports the status to exit with, and whether to exit at all. Its output goes
// to the terminal, where a failed write leaves nothing better to do than carry
// on, so write errors are deliberately ignored.
func runMain(opt Options, invalid error, suite func(*Process) error, stdout, stderr io.Writer) (code int, exit bool) {
	if invalid != nil {
		_, _ = fmt.Fprintln(stderr, invalid)
		return 1, true
	}
	if opt.Progress == nil {
		opt.Progress = func(r Results) {
			_, _ = fmt.Fprintf(stderr, "multiproc: %d processes done\n", r.Processes)
		}
	}
	res, err := Run(opt, suite)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1, true
	}
	if res.Child {
		return 0, true
	}
	_, _ = fmt.Fprintln(stdout, res)
	return 0, false
}

// RunTest runs the pairs in several processes from within a test and returns
// the pooled results.
//
// Parameters: t is the calling test; opt configures the processes, see
// [Options]; pairs are the comparisons, see [Pair].
//
// It does for a test what [Main] does for a program. Unless opt.Args is set,
// the children run only the calling test, since otherwise every test of the
// package would run in every child. In a child, RunTest runs the pairs once and
// skips the rest of the test, which the parent reports; in the parent, an error
// fails the test. The children re-run the test function up to the call, so
// build the candidates' data inside the pairs' builders rather than before.
//
//	func TestLookup(t *testing.T) {
//		res := multiproc.RunTest(t, multiproc.Options{}, multiproc.Pair{Name: "lookup", A: buildA, B: buildB})
//		if p := res.Comparisons[0].Pooled; p.Resolved { ... }
//	}
func RunTest(t testing.TB, opt Options, pairs ...Pair) Results {
	t.Helper()
	return runTest(t, opt, checkPairs(pairs), Pairs(pairs...))
}

// RunTestSuite is [RunTest] for a suite instead of pairs, such as the workload
// package's Suite or several suites combined with [Suites].
//
// Parameters: t is the calling test; opt configures the processes, see
// [Options]; suite is the measurement one process performs, see [Run].
//
// It behaves like RunTest in every other respect.
//
//	func TestSets(t *testing.T) {
//		res := multiproc.RunTestSuite(t, multiproc.Options{},
//			workload.Suite("map vs Set3", 100_000, goMap, set3Set, workload.Options{}))
//		...
//	}
func RunTestSuite(t testing.TB, opt Options, suite func(*Process) error) Results {
	t.Helper()
	return runTest(t, opt, checkSuite(suite), suite)
}

// runTest is what RunTest and RunTestSuite share.
func runTest(t testing.TB, opt Options, invalid error, suite func(*Process) error) Results {
	t.Helper()
	if invalid != nil {
		t.Fatal(invalid)
	}
	if opt.Args == nil {
		opt.Args = []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"}
	}
	res, err := Run(opt, suite)
	if res.Child {
		// The parent reads any error from the child's results file.
		t.SkipNow()
	}
	if err != nil {
		t.Fatal(err)
	}
	return res
}
