package workload_test

import (
	"fmt"
	"strings"
	"testing"

	set3 "github.com/TomTonic/Set3"
	"github.com/TomTonic/rtcompare"
	"github.com/TomTonic/rtcompare/workload"
)

// goMap and set3Set describe the two sets the example compares. Both are
// created with a capacity hint for the elements at rest, as a program that
// knows its size would.
func goMap(target int) workload.Structure[map[uint64]struct{}] {
	return workload.Structure[map[uint64]struct{}]{
		Name: "map",
		New:  func() map[uint64]struct{} { return make(map[uint64]struct{}, target) },
		Apply: func(m map[uint64]struct{}, run []workload.Op) {
			for _, op := range run {
				if op.Kind == workload.Insert {
					m[uint64(op.ID)] = struct{}{}
				} else {
					delete(m, uint64(op.ID))
				}
			}
		},
	}
}

func set3Set(target int) workload.Structure[*set3.Set3[uint64]] {
	return workload.Structure[*set3.Set3[uint64]]{
		Name: "Set3",
		New:  func() *set3.Set3[uint64] { return set3.EmptyWithCapacity[uint64](uint32(target)) },
		Apply: func(s *set3.Set3[uint64], run []workload.Op) {
			for _, op := range run {
				if op.Kind == workload.Insert {
					s.Add(uint64(op.ID))
				} else {
					s.Remove(uint64(op.ID))
				}
			}
		},
	}
}

// ExampleCompare compares Go's built-in map with Set3 under a realistic mix of
// insertions and deletions over 100,000 elements, and answers two questions:
// what an insertion or deletion costs once a set is in use, and what it costs
// to build one. Everything else, the streams, building both sets, the
// untimed first pass and the cursors, is done by Compare.
func ExampleCompare() {
	const target = 100_000
	res, err := workload.Compare(target, goMap(target), set3Set(target), workload.Options{})
	if err != nil {
		panic(err)
	}
	fmt.Println(res)
}

// TestCompareAnswersBothQuestions runs a reduced version of ExampleCompare,
// which is not run by go test since its output varies. It checks the one-call
// comparison of two data structures end to end: both the steady-state and the
// build comparison have to run on real sets and be reported under their own
// headings, and SkipBuild has to leave the build comparison out.
func TestCompareAnswersBothQuestions(t *testing.T) {
	const target = 3000
	quick := rtcompare.CompareOptions{
		Collect:        rtcompare.CollectOptions{Repeats: 11, InnerLoops: 500},
		SkipValidation: true,
		Resamples:      300,
	}
	res, err := workload.Compare(target, goMap(target), set3Set(target), workload.Options{
		SteadyState: quick,
		Build:       rtcompare.CompareOptions{Collect: rtcompare.CollectOptions{Repeats: 11, InnerLoops: 1}, SkipValidation: true, Resamples: 300},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.SteadyState.SamplesA) != 11 || len(res.Build.SamplesA) != 11 {
		t.Errorf("expected 11 samples in each comparison, got %d and %d", len(res.SteadyState.SamplesA), len(res.Build.SamplesA))
	}
	if res.CycleOps == 0 || res.BuildOps == 0 || res.Target != target {
		t.Errorf("stream lengths not reported: %+v", res)
	}
	// A whole build of thousands of elements costs far more than one of its
	// operations.
	if res.Build.NsPerOpA < 100*res.SteadyState.NsPerOpA {
		t.Errorf("a build (%.0f ns) should cost far more than one operation (%.1f ns)", res.Build.NsPerOpA, res.SteadyState.NsPerOpA)
	}
	out := res.String()
	for _, want := range []string{"steady state, per insertion or deletion", "build from empty to 3000 elements, per whole build"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	res, err = workload.Compare(target, goMap(target), set3Set(target), workload.Options{SteadyState: quick, SkipBuild: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Build.SamplesA != nil || strings.Contains(res.String(), "build from empty") {
		t.Errorf("SkipBuild should leave the build comparison out:\n%s", res)
	}
}
