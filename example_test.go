package rtcompare_test

import (
	"fmt"
	"time"

	"github.com/TomTonic/rtcompare"
)

// exampleSink keeps the candidates' work observable.
var exampleSink uint64

// work returns a candidate that performs cost units of arithmetic per
// operation, with the loop in the batch, as rtcompare expects.
func work(name string, cost uint64) rtcompare.Candidate {
	return rtcompare.Candidate{Name: name, Batch: func(n uint64) {
		var acc uint64
		for i := range n * cost {
			acc = acc*31 + i
		}
		exampleSink ^= acc
	}}
}

// Compare measures two candidates and says whether one is genuinely faster.
// Here B does twice A's work. Printing the whole report shows the
// difference, its interval, the noise floor and any warnings; this example
// prints only the verdict, which is the same on every machine.
func ExampleCompare() {
	report, err := rtcompare.Compare(work("single", 1), work("double", 2), rtcompare.CompareOptions{
		// Shortened for the example; the defaults validate more carefully.
		Collect:        rtcompare.CollectOptions{WarmupDuration: 10 * time.Millisecond},
		ValidationRuns: 10,
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("resolved:", report.Resolved)
	fmt.Println("A is faster:", report.Estimate.Delta > 0)
	// Output:
	// resolved: true
	// A is faster: true
}

// EstimateDifference puts an interval around the difference between two sets
// of measurements. A seed makes the interval reproducible, and Paired says
// that A[i] and B[i] were measured together.
func ExampleEstimateDifference() {
	a := []float64{100, 102, 99, 101, 100, 103, 98, 100, 101, 99, 102}
	b := []float64{125, 128, 124, 126, 125, 129, 122, 125, 127, 124, 127}
	e, err := rtcompare.EstimateDifference(a, b, rtcompare.EstimateOptions{Seed: 1, Paired: true})
	if err != nil {
		fmt.Println(err)
		return
	}
	ratio, _, _ := e.Ratio()
	fmt.Printf("A needs %.0f%% less time than B, B takes %.2f× as long\n", e.Delta*100, ratio)
	fmt.Println("difference established:", e.Excludes(0))
	// Output:
	// A needs 20% less time than B, B takes 1.25× as long
	// difference established: true
}

// Combine pools the reports of one comparison run in several processes, each
// process counting as one observation. Here five processes agree on about 12%
// but scatter far more than their own intervals claim, which the pooled
// result reports as inflation.
func ExampleCombine() {
	var reports []rtcompare.Report
	for _, d := range []float64{0.10, 0.14, 0.12, 0.11, 0.13} {
		reports = append(reports, rtcompare.Report{
			Estimate: rtcompare.Estimate{Delta: d, Low: d - 0.002, High: d + 0.002, Level: 0.95},
		})
	}
	p, err := rtcompare.Combine(reports, 0)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Printf("pooled %+.1f%% [%+.1f%%, %+.1f%%]\n", p.Delta*100, p.Low*100, p.High*100)
	fmt.Printf("processes scatter %.0f times as widely as one process's interval says\n", p.Inflation)
	// Output:
	// pooled +12.0% [+10.0%, +14.0%]
	// processes scatter 15 times as widely as one process's interval says
}

// NewDPRNG gives every seed its own fixed sequence, so inputs generated from
// it are the same in every run.
func ExampleNewDPRNG() {
	a, b := rtcompare.NewDPRNG(42), rtcompare.NewDPRNG(42)
	same := true
	for range 1000 {
		if a.Uint32N(100) != b.Uint32N(100) {
			same = false
		}
	}
	fmt.Println("same sequence from the same seed:", same)
	// Output:
	// same sequence from the same seed: true
}

// Shuffle puts fixtures in a seeded order, e.g. the order in which a
// benchmark builds its data structures in each process.
func ExampleDPRNG_Shuffle() {
	rng := rtcompare.NewDPRNG(7)
	order := []string{"A", "B", "C", "D"}
	rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
	again := rtcompare.NewDPRNG(7)
	repeat := []string{"A", "B", "C", "D"}
	again.Shuffle(len(repeat), func(i, j int) { repeat[i], repeat[j] = repeat[j], repeat[i] })
	fmt.Println("reproducible:", fmt.Sprint(order) == fmt.Sprint(repeat))
	// Output:
	// reproducible: true
}
