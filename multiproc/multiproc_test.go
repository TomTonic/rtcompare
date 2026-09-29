package multiproc

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/TomTonic/rtcompare"
)

// onlyThisTest makes the children run nothing but the calling test.
func onlyThisTest(t *testing.T) []string {
	return []string{"-test.run=^" + regexp.QuoteMeta(t.Name()) + "$"}
}

// fakeReport stands in for a real comparison, so that these tests exercise
// the process machinery without spending seconds on measurement. The delta
// varies with the seed like a layout effect would.
func fakeReport(p *Process) rtcompare.Report {
	d := 0.05 + float64(p.Seed%7)*0.002
	return rtcompare.Report{
		NsPerOpA:   float64(p.Index),
		Estimate:   rtcompare.Estimate{Delta: d, Low: d - 0.004, High: d + 0.004, Level: 0.95},
		Validated:  true,
		NoiseFloor: 0.004,
		Resolved:   true,
	}
}

// TestRunPoolsChildProcesses checks the whole round trip a user relies on: the
// suite runs in separate child processes, each with its own seed, and the
// parent gets back one pooled result per named comparison. It belongs to the
// multiproc driver around rtcompare.Combine. With a loose stop rule the run is
// expected to stop at MinProcesses, with every child's report in process
// order, distinct seeds, and a pooled interval over them.
func TestRunPoolsChildProcesses(t *testing.T) {
	res, err := Run(Options{MinProcesses: 3, MaxProcesses: 6, Rotation: 1, AbsPrecision: 0.5, Seed: 99, Args: onlyThisTest(t)},
		func(p *Process) error {
			p.Record("lookup", fakeReport(p))
			p.Record("insert", fakeReport(p))
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	t.Log("\n" + res.String())
	if res.Processes != 3 || !res.Precise {
		t.Errorf("expected to stop precisely after 3 processes, got %d (precise %v)", res.Processes, res.Precise)
	}
	if len(res.Seeds) != 3 || res.Seeds[0] == res.Seeds[1] || res.Seeds[1] == res.Seeds[2] {
		t.Errorf("expected three distinct seeds, got %v", res.Seeds)
	}
	if len(res.Comparisons) != 2 || res.Comparisons[0].Name != "lookup" || res.Comparisons[1].Name != "insert" {
		t.Fatalf("expected the comparisons lookup and insert in that order, got %+v", res.Comparisons)
	}
	for _, c := range res.Comparisons {
		if len(c.Reports) != 3 || c.Pooled.Processes != 3 {
			t.Errorf("%s: %d reports, pooled over %d", c.Name, len(c.Reports), c.Pooled.Processes)
		}
		for i, r := range c.Reports {
			if r.NsPerOpA != float64(i) {
				t.Errorf("%s: report %d came from process %v", c.Name, i, r.NsPerOpA)
			}
		}
	}
}

// TestRunIsReproducibleFromItsSeed checks that a whole multi-process run can
// be repeated: the same Options.Seed has to hand the same seeds to the same
// processes, and a different one different seeds.
func TestRunIsReproducibleFromItsSeed(t *testing.T) {
	seeds := func(seed uint64) []uint64 {
		res, err := Run(Options{MinProcesses: 3, MaxProcesses: 3, Rotation: 1, AbsPrecision: 0.5, Seed: seed, Args: onlyThisTest(t)},
			func(p *Process) error {
				p.Record("x", fakeReport(p))
				return nil
			})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return res.Seeds
	}
	a := seeds(1)
	if a == nil {
		return // in a child
	}
	if b := seeds(1); !slices.Equal(a, b) {
		t.Errorf("the same seed gave %v and %v", a, b)
	}
	if c := seeds(2); a[0] == c[0] {
		t.Errorf("seeds 1 and 2 gave the same first process seed %d", a[0])
	}
}

// TestRunKeepsGoingUntilPrecise checks the adaptive stop rule: when the
// processes disagree more than the requested precision allows, the driver has
// to keep starting processes up to MaxProcesses and then say that it stopped
// short.
func TestRunKeepsGoingUntilPrecise(t *testing.T) {
	res, err := Run(Options{MinProcesses: 3, MaxProcesses: 4, Rotation: 1, AbsPrecision: 1e-9, RelPrecision: 1e-9, Args: onlyThisTest(t)},
		func(p *Process) error {
			r := fakeReport(p)
			r.Estimate.Delta += float64(p.Index%2) * 0.05
			p.Record("scattered", r)
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	if res.Processes != 4 || res.Precise {
		t.Errorf("expected to run all 4 processes and stop imprecise, got %d (precise %v)", res.Processes, res.Precise)
	}
	if !strings.Contains(res.String(), "stopped before") {
		t.Errorf("the summary should say the run stopped short:\n%s", res)
	}
}

// TestRunReportsChildFailures checks that a failing suite does not vanish into
// a child's exit status: its error has to reach the caller in the parent, with
// the process named, and so does a suite that recorded nothing.
func TestRunReportsChildFailures(t *testing.T) {
	cases := []struct {
		name  string
		suite func(*Process) error
		want  string
	}{
		{"returns the suite's error", func(*Process) error { return errors.New("fixture exploded") }, "fixture exploded"},
		{"returns error when nothing was recorded", func(*Process) error { return nil }, "recorded no comparisons"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := Run(Options{MinProcesses: 3, Args: onlyThisTest(t), Stderr: &strings.Builder{}}, c.suite)
			if res.Child {
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), "process 0") {
				t.Errorf("got %v, want an error about process 0 mentioning %q", err, c.want)
			}
		})
	}
}

// TestOptionsResolve checks that the driver rejects settings under which
// pooling cannot mean anything, and fills in its documented defaults.
func TestOptionsResolve(t *testing.T) {
	bad := []struct {
		name string
		opt  Options
		want string
	}{
		{"returns error for fewer than three processes", Options{MinProcesses: 2}, "at least 3"},
		{"returns error for a maximum below the minimum", Options{MinProcesses: 6, MaxProcesses: 5}, "must not be below"},
		{"returns error for a negative precision", Options{AbsPrecision: -1}, "positive"},
		{"returns error for a negative rotation", Options{Rotation: -2}, "Rotation"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			if _, err := c.opt.resolve(); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
	opt, err := Options{MinProcesses: 50}.resolve()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opt.MaxProcesses != 50 || opt.Rotation != DefaultRotation || opt.AbsPrecision != DefaultAbsPrecision || opt.RelPrecision != DefaultRelPrecision ||
		opt.Seed == 0 || opt.Executable == "" || opt.Args == nil || opt.Stdout == nil || opt.Stderr == nil {
		t.Errorf("defaults not filled in: %+v", opt)
	}
}

// TestProcessRandIsSeeded checks that the generator a child uses to shuffle
// its build order is reproducible from the process seed, including seed zero,
// which rtcompare.NewDPRNG would otherwise take as a request for a random one.
func TestProcessRandIsSeeded(t *testing.T) {
	for _, seed := range []uint64{0, 1, 12345} {
		a, b := (&Process{Seed: seed}).Rand(), (&Process{Seed: seed}).Rand()
		if a.Uint64() != b.Uint64() {
			t.Errorf("seed %d gave two different streams", seed)
		}
	}
}

// TestRunStopsOnlyAfterAWholeRotation checks that a suite alternating its
// build order between processes gets every order equally often in the pooled
// result. With a stop rule that is met at once, MinProcesses 3 and the default
// rotation of 2, the driver has to run a fourth process rather than stop with
// one order represented twice and the other once.
func TestRunStopsOnlyAfterAWholeRotation(t *testing.T) {
	res, err := Run(Options{MinProcesses: 3, AbsPrecision: 0.5, Args: onlyThisTest(t)},
		func(p *Process) error {
			p.Record("x", fakeReport(p))
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	if res.Processes != 4 || !res.Precise {
		t.Errorf("expected to stop precisely after 4 processes, got %d (precise %v)", res.Processes, res.Precise)
	}
}

// TestRunSizesItselfFromTheFirstStage checks how a run decides how many
// processes it needs: once, from the scatter of its first stage, and not by
// looking at the interval after every process, which would stop whenever the
// processes happened to agree. Six processes scattering by a sample standard
// deviation of 0.0329 need (2.5706*0.0329/0.02)² = 17.8, so 18 processes, and
// the run has to run exactly those, even though every later process agrees
// exactly, and report Stein's interval from the first stage.
func TestRunSizesItselfFromTheFirstStage(t *testing.T) {
	res, err := Run(Options{MinProcesses: 6, Args: onlyThisTest(t)},
		func(p *Process) error {
			d := 0.05
			if p.Index < 6 {
				d += 0.03 * float64(1-2*(p.Index%2))
			}
			p.Record("x", rtcompare.Report{Estimate: rtcompare.Estimate{Delta: d, Low: d - 0.001, High: d + 0.001, Level: 0.95}})
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	p := res.Comparisons[0].Pooled
	if res.Processes != 18 || !res.Precise || p.Processes != 18 || p.FirstStage != 6 {
		t.Fatalf("ran %d processes (precise %v), pooled %d with first stage %d; want 18, true, 18, 6", res.Processes, res.Precise, p.Processes, p.FirstStage)
	}
	if half, want := (p.High-p.Low)/2, 2.570582*0.0328634/math.Sqrt(18); math.Abs(half-want) > 1e-5 {
		t.Errorf("half-width %.5f, want Stein's %.5f", half, want)
	}
}

// TestRecordCarriesTheAADifferences checks that what a child hands to its
// parent keeps the signed A/A differences, which the pooled noise floor is
// built from, through the JSON round trip.
func TestRecordCarriesTheAADifferences(t *testing.T) {
	var r rtcompare.Report
	r.Validated = true
	r.ValidationA.Deltas = []float64{0.001, -0.002}
	r.ValidationB.Deltas = []float64{0.003}
	data, err := json.Marshal(newRecord("x", r))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var back record
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := back.report()
	if !slices.Equal(got.ValidationA.Deltas, r.ValidationA.Deltas) || !slices.Equal(got.ValidationB.Deltas, r.ValidationB.Deltas) {
		t.Errorf("A/A differences after the round trip: %v and %v", got.ValidationA.Deltas, got.ValidationB.Deltas)
	}
}
