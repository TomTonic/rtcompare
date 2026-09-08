// Command rtcompare-example compares two median implementations and reports
// whether the difference between them is one this machine can actually resolve.
//
// The comparison itself is the least interesting part. What the example is
// really about is everything around it: sizing the batches so that a difference
// below the clock's resolution is measurable at all, finding out what the
// harness invents on its own before trusting it, and noticing that the two
// candidates are not equally well behaved. rtcompare.Compare does all of that in
// one call and reports what it decided, which is what the first half of this
// program shows. The second half takes the same measurements apart by hand, for
// when the summary is not enough.
package main

import (
	"fmt"
	"os"

	"github.com/TomTonic/rtcompare"
)

// sink absorbs the results of measured work. Assigning to a package-level
// variable keeps a candidate honest; it is cheap insurance rather than a
// necessity, since the Go compiler does not delete a loop merely because
// nothing reads what it computes.
var sink float64

const arraySize = 50

func main() {
	// Both candidates refresh their input inside the loop, because QuickMedian
	// mutates what it is given and the two must do the same work per operation
	// to be comparable. That refresh is measured along with the candidate and
	// dilutes the difference between them; see the note on attenuation in the
	// Collect documentation. It cannot be hoisted into Setup, because it has to
	// happen per operation rather than per batch.
	quick := medianCandidate("QuickMedian", rtcompare.QuickMedian)
	sorting := medianCandidate("Median", rtcompare.Median)

	// Everything is left at its default: the batches are sized so the clock
	// contributes at most a tenth of a percent, both candidates are validated
	// against themselves, the measurement order is interleaved, and the
	// resampling scheme is chosen from the dependence actually observed.
	fmt.Println("Comparing QuickMedian against Median. This validates the")
	fmt.Println("harness against each candidate first, so it takes a few seconds.")

	report, err := rtcompare.Compare(quick, sorting, rtcompare.CompareOptions{
		Collect:    rtcompare.CollectOptions{GCBetween: true},
		Thresholds: []float64{0.05, 0.10, 0.20, 0.30},
	})
	if err != nil {
		fail("comparing: %v", err)
	}

	fmt.Printf("\n%s\n", report)

	if !report.Resolved {
		fmt.Println("\nNothing has been resolved; the run says only that any difference is small.")
		return
	}

	// The report carries the evidence as well as the verdict, so the pieces are
	// there when the summary is not enough.
	fmt.Println("\n--- the evidence behind that verdict ---")

	// How large is the difference, and how precisely is that known? This is the
	// question to ask when no threshold was given to you.
	e := report.Estimate
	fmt.Printf("\nEstimated difference: %s\n", e)
	fmt.Printf("  point estimate %+.2f%%, bootstrap median %+.2f%% (a large gap would mean the\n"+
		"  statistic behaves awkwardly on this data and the interval deserves suspicion)\n",
		e.Delta*100, e.BootstrapMedian*100)
	fmt.Printf("  the interval %s zero, so a difference %s established\n",
		yesNo(e.Excludes(0), "excludes", "includes"),
		yesNo(e.Excludes(0), "has been", "has not been"))

	// What the harness does to identical code, which is what the result above
	// has to be read against.
	fmt.Printf("\nNoise floor across both candidates: %.2f%%\n", report.NoiseFloor*100)
	for _, v := range []struct {
		name string
		val  rtcompare.HarnessValidation
	}{{"QuickMedian", report.ValidationA}, {"Median", report.ValidationB}} {
		fmt.Printf("\n%s:\n%s\n", v.name, v.val)
	}

	// A trend across a run is invisible to the bootstrap, which treats the
	// samples as an unordered bag, so Compare tests for it separately.
	for _, d := range []struct {
		name string
		rep  rtcompare.DriftReport
	}{{"QuickMedian", report.DriftA}, {"Median", report.DriftB}} {
		if d.rep.N > 0 && d.rep.Drifted(0.05) {
			fmt.Printf("\nnote: %s %s\n", d.name, d.rep)
		}
	}

	if report.BlockLength > 1 {
		fmt.Printf("\nThe samples were correlated with their neighbours (%+.3f), so they were\n"+
			"resampled in blocks of %d rather than one at a time. Median allocates a copy on\n"+
			"every call, which tends to put it there.\n", report.Autocorrelation, report.BlockLength)
	}

	fmt.Println("\nConfidence that QuickMedian beats Median by at least:")
	for _, t := range []float64{0.05, 0.10, 0.20, 0.30} {
		fmt.Printf("  %6.2f%%   %7.2f%%\n", t*100, report.Confidence[t]*100)
	}
}

// medianCandidate wraps one median implementation as a candidate.
func medianCandidate(name string, median func([]float64) float64) rtcompare.Candidate {
	return rtcompare.Candidate{
		Name: name,
		Batch: func(n uint64) {
			rng := rtcompare.NewDPRNG(0x5EED)
			work := make([]float64, arraySize)
			var acc float64
			for range n {
				// Constant cost in the array's length, so it adds the same
				// amount to both candidates.
				for i := range work {
					work[i] = rng.Float64()
				}
				acc += median(work)
			}
			sink += acc
		},
	}
}

func yesNo(cond bool, yes, no string) string {
	if cond {
		return yes
	}
	return no
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rtcompare-example: "+format+"\n", args...)
	os.Exit(1)
}
