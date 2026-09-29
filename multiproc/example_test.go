package multiproc_test

import (
	"testing"

	"github.com/TomTonic/rtcompare"
	"github.com/TomTonic/rtcompare/multiproc"
)

// tree stands for a data structure large enough that one process is one
// observation, and lookupIn for the candidate that measures it.
type tree struct{ keys []uint64 }

func buildTree() *tree { return &tree{keys: make([]uint64, 1<<20)} }

func lookupIn(t *tree) rtcompare.Candidate {
	return rtcompare.Candidate{Name: "lookup", Batch: func(n uint64) {
		for i := range n {
			_ = t.keys[i%uint64(len(t.keys))]
		}
	}}
}

// Main is the whole of a benchmark program: it runs the comparison in child
// processes, each with its own heap layout and alternating build order, and
// prints the pooled result. Build the data inside the builders, so that it is
// built in every child, after the heap is perturbed.
func ExampleMain() {
	multiproc.Main(multiproc.Options{}, multiproc.Pair{
		Name: "lookup",
		A:    func() rtcompare.Candidate { return lookupIn(buildTree()) },
		B:    func() rtcompare.Candidate { return lookupIn(buildTree()) },
	})
}

// RunTest does the same inside a test and returns the pooled results for
// assertions; the children run only the calling test.
func ExampleRunTest() {
	var t *testing.T // the test's own t
	res := multiproc.RunTest(t, multiproc.Options{Parallel: 4}, multiproc.Pair{
		Name: "lookup",
		A:    func() rtcompare.Candidate { return lookupIn(buildTree()) },
		B:    func() rtcompare.Candidate { return lookupIn(buildTree()) },
	})
	if p := res.Comparisons[0].Pooled; !p.Resolved {
		t.Log("no difference established:", p)
	}
}
