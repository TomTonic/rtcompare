package multiproc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordIndex is a suite whose report carries the process index in NsPerOpA,
// so that tests can see which child a report came from.
func recordIndex(p *Process) error {
	p.Record("x", fakeReport(p))
	return nil
}

// progressCounter collects the process count at every Progress call.
func progressCounter() (*[]int, func(Results)) {
	var calls []int
	return &calls, func(r Results) { calls = append(calls, r.Processes) }
}

// TestSerialRegimeIsUnchanged checks that runs which do not ask for
// parallelism behave exactly as before parallel runs existed: Parallel 0 and
// 1 both run one process at a time, report after every process, stop only
// after a whole Rotation, set no GOMAXPROCS of their own and report
// Results.Parallel as 1.
func TestSerialRegimeIsUnchanged(t *testing.T) {
	for _, parallel := range []int{0, 1} {
		calls, progress := progressCounter()
		res, err := Run(Options{MinProcesses: 3, AbsPrecision: 0.5, Parallel: parallel, Progress: progress, Args: onlyThisTest(t)}, recordIndex)
		if err != nil {
			t.Fatalf("Parallel %d: unexpected error: %v", parallel, err)
		}
		if res.Child {
			return
		}
		if res.Processes != 4 || !slices.Equal(*calls, []int{1, 2, 3, 4}) || res.Parallel != 1 {
			t.Errorf("Parallel %d: %d processes, progress %v, Parallel %d; want 4, [1 2 3 4], 1",
				parallel, res.Processes, *calls, res.Parallel)
		}
		want, _ := strconv.Atoi(os.Getenv("GOMAXPROCS"))
		if res.ChildGOMAXPROCS != want {
			t.Errorf("Parallel %d: ChildGOMAXPROCS %d, want the caller's %d", parallel, res.ChildGOMAXPROCS, want)
		}
	}
}

// TestParallelRunsInWholeWaves checks the parallel regime's rhythm: processes
// start in waves of Parallel, the stop rule and Progress see only complete
// waves, and a run stopped by its budget ends on a wave boundary.
func TestParallelRunsInWholeWaves(t *testing.T) {
	calls, progress := progressCounter()
	res, err := Run(Options{MinProcesses: 3, AbsPrecision: 0.5, Parallel: 4, Progress: progress, Args: onlyThisTest(t)}, recordIndex)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	if res.Processes != 4 || !res.Precise || !slices.Equal(*calls, []int{4}) || res.Parallel != 4 {
		t.Errorf("precise at once: %d processes, precise %v, progress %v, Parallel %d; want 4, true, [4], 4",
			res.Processes, res.Precise, *calls, res.Parallel)
	}

	calls, progress = progressCounter()
	res, err = Run(Options{MinProcesses: 3, MaxProcesses: 8, AbsPrecision: 1e-9, RelPrecision: 1e-9, Parallel: 4, Progress: progress, Args: onlyThisTest(t)},
		func(p *Process) error {
			r := fakeReport(p)
			r.Estimate.Delta += float64(p.Index%2) * 0.05
			p.Record("scattered", r)
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Processes != 8 || res.Precise || !slices.Equal(*calls, []int{4, 8}) {
		t.Errorf("never precise: %d processes, precise %v, progress %v; want 8, false, [4 8]", res.Processes, res.Precise, *calls)
	}
}

// TestParallelIsRepeatableFromItsSeed checks that running processes at the
// same time does not change which processes run: the seeds and indexes of a
// parallel run equal those of a serial run with the same Seed and number of
// processes, and the reports are pooled in index order, whichever child
// finished first.
func TestParallelIsRepeatableFromItsSeed(t *testing.T) {
	run := func(parallel int) Results {
		res, err := Run(Options{MinProcesses: 4, MaxProcesses: 4, Rotation: 1, AbsPrecision: 0.5, Seed: 42, Parallel: parallel, Args: onlyThisTest(t)}, recordIndex)
		if err != nil {
			t.Fatalf("Parallel %d: unexpected error: %v", parallel, err)
		}
		return res
	}
	serial := run(1)
	if serial.Child {
		return
	}
	parallel := run(4)
	if !slices.Equal(serial.Seeds, parallel.Seeds) {
		t.Errorf("seeds differ: serial %v, parallel %v", serial.Seeds, parallel.Seeds)
	}
	for i, r := range parallel.Comparisons[0].Reports {
		if r.NsPerOpA != float64(i) {
			t.Errorf("report %d came from process %v; reports must be in index order", i, r.NsPerOpA)
		}
	}
}

// TestParallelChildrenOverlap checks that a parallel run actually runs its
// children at the same time: each child of a wave records when it started
// and ended, and every start has to come before every end.
func TestParallelChildrenOverlap(t *testing.T) {
	res, err := Run(Options{MinProcesses: 4, MaxProcesses: 4, AbsPrecision: 0.5, Parallel: 4, Args: onlyThisTest(t)},
		func(p *Process) error {
			r := fakeReport(p)
			r.NsPerOpA = float64(time.Now().UnixMilli())
			time.Sleep(500 * time.Millisecond)
			r.NsPerOpB = float64(time.Now().UnixMilli())
			p.Record("x", r)
			return nil
		})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	lastStart, firstEnd := 0.0, float64(time.Now().UnixMilli())
	for _, r := range res.Comparisons[0].Reports {
		lastStart = max(lastStart, r.NsPerOpA)
		firstEnd = min(firstEnd, r.NsPerOpB)
	}
	if lastStart >= firstEnd {
		t.Errorf("the children did not overlap: the last started at %v, the first ended at %v", lastStart, firstEnd)
	}
}

// TestParallelFailureCancelsTheWave checks what happens when one child of a
// wave fails: its siblings are killed rather than left to finish, and the
// error returned is the one of the lowest failing index, not the kill signal
// of a sibling that was merely cancelled.
func TestParallelFailureCancelsTheWave(t *testing.T) {
	start := time.Now()
	res, err := Run(Options{MinProcesses: 4, MaxProcesses: 4, Parallel: 4, Args: onlyThisTest(t), Stderr: &strings.Builder{}},
		func(p *Process) error {
			switch p.Index {
			case 2:
				return errors.New("boom in 2")
			case 3:
				time.Sleep(300 * time.Millisecond)
				return errors.New("boom in 3")
			default:
				time.Sleep(time.Minute)
				return nil
			}
		})
	if res.Child {
		return
	}
	if err == nil || !strings.Contains(err.Error(), "process 2") || !strings.Contains(err.Error(), "boom in 2") {
		t.Errorf("got %v, want the error of process 2", err)
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Errorf("Run took %v; the sleeping siblings should have been killed", elapsed)
	}
}

// TestParallelChildrenShareTheCPUs checks that the children of a parallel run
// get a GOMAXPROCS that shares the machine among them, so that one child's
// garbage collector cannot take cores from its neighbours, and that the value
// is reported in Results.
func TestParallelChildrenShareTheCPUs(t *testing.T) {
	if os.Getenv("GOMAXPROCS") != "" && os.Getenv(envOut) == "" {
		t.Skip("the caller's GOMAXPROCS takes precedence; see TestParallelRespectsCallerGOMAXPROCS")
	}
	res, err := Run(Options{MinProcesses: 4, MaxProcesses: 4, AbsPrecision: 0.5, Parallel: 4, Args: onlyThisTest(t)}, recordGOMAXPROCS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	want := childGOMAXPROCS(runtime.NumCPU(), 4)
	if res.ChildGOMAXPROCS != want {
		t.Errorf("ChildGOMAXPROCS %d, want %d", res.ChildGOMAXPROCS, want)
	}
	for i, r := range res.Comparisons[0].Reports {
		if int(r.NsPerOpA) != want {
			t.Errorf("child %d ran with GOMAXPROCS %v, want %d", i, r.NsPerOpA, want)
		}
	}
}

// TestParallelRespectsCallerGOMAXPROCS checks that a GOMAXPROCS the caller
// set on purpose reaches the children unchanged and is what Results reports.
func TestParallelRespectsCallerGOMAXPROCS(t *testing.T) {
	t.Setenv("GOMAXPROCS", "3")
	res, err := Run(Options{MinProcesses: 4, MaxProcesses: 4, AbsPrecision: 0.5, Parallel: 4, Args: onlyThisTest(t)}, recordGOMAXPROCS)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Child {
		return
	}
	if res.ChildGOMAXPROCS != 3 {
		t.Errorf("ChildGOMAXPROCS %d, want the caller's 3", res.ChildGOMAXPROCS)
	}
	for i, r := range res.Comparisons[0].Reports {
		if r.NsPerOpA != 3 {
			t.Errorf("child %d ran with GOMAXPROCS %v, want 3", i, r.NsPerOpA)
		}
	}
}

func recordGOMAXPROCS(p *Process) error {
	r := fakeReport(p)
	r.NsPerOpA = float64(runtime.GOMAXPROCS(0))
	p.Record("x", r)
	return nil
}

// TestProcessBudgetDefaultsAndRounding checks how the two regimes settle
// their process counts: a serial run rounds its first stage up to a whole
// Rotation and keeps its maximum, with a default budget of 40; a parallel run
// rounds its wave up to a whole Rotation, rounds MinProcesses and
// MaxProcesses up to whole waves, and budgets 10 waves by default.
func TestProcessBudgetDefaultsAndRounding(t *testing.T) {
	cases := []struct {
		name                     string
		opt                      Options
		wave, minProcs, maxProcs int
	}{
		{"serial rounds the default first stage up to a rotation", Options{}, 1, 6, 40},
		{"serial rounds an explicit first stage up to a rotation", Options{MinProcesses: 7, MaxProcesses: 9}, 1, 8, 9},
		{"serial rotation of one keeps explicit counts", Options{MinProcesses: 7, MaxProcesses: 9, Rotation: 1}, 1, 7, 9},
		{"parallel rounds min up and budgets ten waves", Options{Parallel: 4}, 4, 8, 40},
		{"parallel rounds the wave up to a whole rotation", Options{Parallel: 3}, 4, 8, 40},
		{"parallel rotation of one keeps the wave", Options{Parallel: 3, Rotation: 1}, 3, 6, 30},
		{"parallel rounds an explicit maximum up to a wave", Options{Parallel: 12, MaxProcesses: 30}, 12, 12, 36},
		{"parallel budget of twelve", Options{Parallel: 12}, 12, 12, 120},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opt, err := c.opt.resolve()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if opt.Parallel != c.wave || opt.MinProcesses != c.minProcs || opt.MaxProcesses != c.maxProcs {
				t.Errorf("wave %d, min %d, max %d; want %d, %d, %d", opt.Parallel, opt.MinProcesses, opt.MaxProcesses, c.wave, c.minProcs, c.maxProcs)
			}
		})
	}
	if _, err := (Options{Parallel: -1}).resolve(); err == nil || !strings.Contains(err.Error(), "Parallel") {
		t.Errorf("got %v, want an error about a negative Parallel", err)
	}
}

// TestChildGOMAXPROCS checks the share of CPUs each child of a wave gets:
// the CPUs divided among the children, and never fewer than two.
func TestChildGOMAXPROCS(t *testing.T) {
	for _, c := range []struct{ cpus, parallel, want int }{{24, 12, 2}, {24, 4, 6}, {4, 8, 2}, {16, 3, 5}} {
		if got := childGOMAXPROCS(c.cpus, c.parallel); got != c.want {
			t.Errorf("childGOMAXPROCS(%d, %d) = %d, want %d", c.cpus, c.parallel, got, c.want)
		}
	}
}

// TestLineWriterKeepsLinesWhole checks the output of concurrent children:
// written through writers that share a lock, every line arrives whole and
// with its child's prefix, however the writes were split, and a last line
// without a newline is completed when the child ends.
func TestLineWriterKeepsLinesWhole(t *testing.T) {
	var out bytes.Buffer
	w := &waveRunner{opt: Options{Parallel: 2, Stdout: &out, Stderr: &out}, width: 2}
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, _, flush := w.writers(i)
			for range 200 {
				_, _ = stdout.Write([]byte("hello "))
				_, _ = stdout.Write([]byte("world\n"))
			}
			_, _ = stdout.Write([]byte("tail"))
			flush()
		}()
	}
	wg.Wait()
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 402 {
		t.Fatalf("got %d lines, want 402", len(lines))
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "[p00] ") && !strings.HasPrefix(line, "[p01] ") {
			t.Fatalf("line without a prefix: %q", line)
		}
		if body := line[6:]; body != "hello world" && body != "tail" {
			t.Fatalf("line broken up: %q", line)
		}
	}
	if w.lineWriter(io.Discard, "[p00] ") != io.Discard {
		t.Error("io.Discard should be passed through unwrapped")
	}
}
