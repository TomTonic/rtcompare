// Command rtcompare-example compares two median implementations and reports
// whether the difference between them is one this machine can actually resolve.
//
// The comparison itself is the least interesting part. What the example is
// really about is the work around it: measuring what the harness invents on its
// own before trusting it, noticing that the two candidates are not equally well
// behaved, and choosing the resampling scheme from what was measured rather than
// from habit.
package main

import (
	"fmt"
	"os"
	"slices"

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

	// Leaving InnerLoops at zero lets Collect size the batches itself, so that
	// the clock contributes at most a tenth of a percent to each measurement.
	// Everything else is the default: repeats, ABBA interleaving, one warm-up.
	opts := rtcompare.CollectOptions{GCBetween: true}

	// Validate both candidates, not just one. They need not be equally well
	// behaved, and the comparison is only as trustworthy as the worse of them.
	fmt.Println("Validating the harness against each candidate...")
	worstFloor, worstAuto := 0.0, 0.0
	for _, c := range []rtcompare.Candidate{quick, sorting} {
		v, err := rtcompare.ValidateHarness(c, rtcompare.ValidationOptions{Collect: opts})
		if err != nil {
			fail("validating %s: %v", c.Name, err)
		}
		fmt.Printf("\n%s:\n%s\n", c.Name, v)
		worstFloor = max(worstFloor, v.NoiseFloor)
		worstAuto = max(worstAuto, v.Autocorrelation)
	}

	fast, slow, err := rtcompare.Collect(quick, sorting, opts)
	if err != nil {
		fail("collecting measurements: %v", err)
	}

	medFast, medSlow := rtcompare.Median(fast), rtcompare.Median(slow)
	observed := 1 - medFast/medSlow
	fmt.Printf("\nQuickMedian %.1f ns/op, Median %.1f ns/op, difference %+.2f%%\n",
		medFast, medSlow, observed*100)
	fmt.Printf("Worst noise floor across the two: %.2f%%\n", worstFloor*100)

	// A trend across a run is invisible to the bootstrap, which treats the
	// samples as an unordered bag, so it has to be checked separately.
	for name, samples := range map[string][]float64{"QuickMedian": fast, "Median": slow} {
		if d, err := rtcompare.DetectDrift(samples); err == nil && d.Drifted(0.05) {
			fmt.Printf("note: %s %s\n", name, d)
		}
	}

	if observed <= worstFloor {
		fmt.Printf("\nThat difference is inside what this machine produces from identical code.\n")
		fmt.Println("Nothing has been resolved; the run says only that any difference is small.")
		return
	}

	// Choose the resampling scheme from the measurement. Beyond a lag-1
	// autocorrelation of about 0.2 the ordinary bootstrap treats the samples as
	// carrying more information than they do; below it, blocks buy nothing.
	// Median allocates a copy on every call, which tends to put it there.
	thresholds := []float64{worstFloor, 0.05, 0.10, 0.20, 0.30}
	slices.Sort(thresholds)
	var confidence map[float64]float64
	if worstAuto > 0.2 {
		fmt.Printf("\nAutocorrelation is %+.3f, so resampling in blocks of %d.\n",
			worstAuto, rtcompare.AutoBlockLength(len(fast)))
		confidence = rtcompare.BlockBootstrapConfidence(fast, slow, thresholds, rtcompare.DefaultResamples, 0, 0)
	} else {
		fmt.Printf("\nAutocorrelation is %+.3f, low enough for ordinary resampling.\n", worstAuto)
		confidence = rtcompare.BootstrapConfidence(fast, slow, thresholds, rtcompare.DefaultResamples, 0)
	}

	fmt.Println("\nConfidence that QuickMedian beats Median by at least:")
	for _, t := range thresholds {
		note := ""
		if t == worstFloor {
			note = "   (the noise floor)"
		}
		fmt.Printf("  %6.2f%%   %7.2f%%%s\n", t*100, confidence[t]*100, note)
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

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "rtcompare-example: "+format+"\n", args...)
	os.Exit(1)
}
