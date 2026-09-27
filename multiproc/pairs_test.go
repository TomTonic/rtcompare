package multiproc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TomTonic/rtcompare"
)

var pairsSink uint64

// spin is a cheap candidate with unremovable work, for pairs that must run a
// real comparison quickly.
func spin(name string, cost uint64) rtcompare.Candidate {
	return rtcompare.Candidate{Name: name, Batch: func(n uint64) {
		var acc uint64
		for i := range n * cost {
			acc = acc*31 + i
		}
		pairsSink ^= acc
	}}
}

// quick keeps a real comparison to a few milliseconds.
var quick = rtcompare.CompareOptions{
	Collect:        rtcompare.CollectOptions{Repeats: 11, InnerLoops: 2000, WarmupDuration: time.Millisecond},
	SkipValidation: true,
	Resamples:      300,
}

// TestPairBuildOrderAlternates checks the automatic protection against the
// build-order effect: whichever candidate's data is built second can be
// consistently faster, so the driver has to build A first in even processes
// and B first in odd ones without the user having to arrange it.
func TestPairBuildOrderAlternates(t *testing.T) {
	var order []string
	pair := Pair{
		Name: "x",
		A:    func() rtcompare.Candidate { order = append(order, "A"); return spin("a", 1) },
		B:    func() rtcompare.Candidate { order = append(order, "B"); return spin("b", 1) },
	}
	for index := range 4 {
		a, b := pair.build(index)
		if a.Name != "a" || b.Name != "b" {
			t.Errorf("process %d: the candidates swapped roles", index)
		}
	}
	if got := strings.Join(order, ""); got != "ABBAABBA" {
		t.Errorf("build order over four processes: got %s, want ABBAABBA", got)
	}
}

// TestPairsRecordsEachComparison checks the suite behind Main and RunTest in a
// single process: every pair is built, compared and recorded under its name,
// in order, and a failing comparison is reported with the pair's name.
func TestPairsRecordsEachComparison(t *testing.T) {
	p := &Process{Index: 1}
	err := Pairs(
		Pair{Name: "first", A: func() rtcompare.Candidate { return spin("a", 1) }, B: func() rtcompare.Candidate { return spin("b", 2) }, Options: quick},
		Pair{Name: "second", A: func() rtcompare.Candidate { return spin("a", 2) }, B: func() rtcompare.Candidate { return spin("b", 1) }, Options: quick},
	)(p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.records) != 2 || p.records[0].Name != "first" || p.records[1].Name != "second" {
		t.Fatalf("expected records first and second, got %+v", p.records)
	}
	if p.records[0].Estimate.Delta <= 0 || p.records[1].Estimate.Delta >= 0 {
		t.Errorf("the cheaper candidate should come out faster: %+v", p.records)
	}

	broken := Pair{Name: "broken", A: func() rtcompare.Candidate { return rtcompare.Candidate{} }, B: func() rtcompare.Candidate { return spin("b", 1) }}
	if err := Pairs(broken)(p); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("got %v, want an error naming the pair", err)
	}
}

// TestCheckPairs checks that pairs which cannot run are rejected before any
// process is started, with the reason named.
func TestCheckPairs(t *testing.T) {
	build := func() rtcompare.Candidate { return spin("x", 1) }
	cases := []struct {
		name  string
		pairs []Pair
		want  string
	}{
		{"returns error for no pairs", nil, "no pairs"},
		{"returns error for a missing name", []Pair{{A: build, B: build}}, "no Name"},
		{"returns error for a duplicate name", []Pair{{Name: "x", A: build, B: build}, {Name: "x", A: build, B: build}}, "two pairs"},
		{"returns error for a missing builder", []Pair{{Name: "x", A: build}}, "both an A and a B"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := checkPairs(c.pairs); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, want an error mentioning %q", err, c.want)
			}
		})
	}
}

// TestRunTestPoolsPairsAcrossProcesses checks the one-call path for tests:
// the pairs run in real child processes, restricted to this test, and come
// back pooled, with nothing to remember about child processes or build order.
func TestRunTestPoolsPairsAcrossProcesses(t *testing.T) {
	res := RunTest(t, Options{MinProcesses: 3, MaxProcesses: 4, AbsPrecision: 0.5},
		Pair{Name: "cheap vs costly", A: func() rtcompare.Candidate { return spin("a", 1) }, B: func() rtcompare.Candidate { return spin("b", 2) }, Options: quick})
	if res.Processes != 4 || len(res.Comparisons) != 1 {
		t.Fatalf("expected one comparison over 4 processes, got %d over %d", len(res.Comparisons), res.Processes)
	}
	if p := res.Comparisons[0].Pooled; p.Processes != 4 || p.Delta <= 0 {
		t.Errorf("pooled result %+v, want A faster over 4 processes", p)
	}
}

// TestRunMainRoles checks what Main does in each role without exiting the
// test binary: invalid pairs end the program with status 1, and a child runs
// the pairs, writes its results for the parent and ends with status 0.
func TestRunMainRoles(t *testing.T) {
	var out, errOut strings.Builder
	if code, exit := runMain(Options{}, nil, &out, &errOut); code != 1 || !exit || !strings.Contains(errOut.String(), "no pairs") {
		t.Errorf("invalid pairs: code %d, exit %v, stderr %q", code, exit, errOut.String())
	}

	file := filepath.Join(t.TempDir(), "child.json")
	t.Setenv(envOut, file)
	t.Setenv(envSeed, "7")
	t.Setenv(envIndex, "0")
	pair := Pair{Name: "x", A: func() rtcompare.Candidate { return spin("a", 1) }, B: func() rtcompare.Candidate { return spin("b", 1) }, Options: quick}
	if code, exit := runMain(Options{}, []Pair{pair}, &out, &errOut); code != 0 || !exit {
		t.Errorf("child: code %d, exit %v, stderr %q", code, exit, errOut.String())
	}
	if data, err := os.ReadFile(file); err != nil || !strings.Contains(string(data), `"name":"x"`) {
		t.Errorf("child results file: %s, %v", data, err)
	}
}
