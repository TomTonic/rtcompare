package main

import (
	"fmt"
	"math/rand"

	"github.com/TomTonic/rtcompare"
)

func main() {
	const (
		N              = 50     // size of input array
		repeats        = 101    // number of timing samples
		innerLoops     = 2000   // number of median calls per timing sample
		precisionLevel = 10_000 // bootstrap repetitions
	)

	rng := rtcompare.DPRNG{State: uint64(rand.Uint64()&0xFFFFFFFFFFFFFFE + 1)} // avoid zero seed
	safeState := rng.State

	workArrayMedian := make([]float64, N)
	fillArray(workArrayMedian, rng) // rng is passed by value here so we should not need to safeguard its state
	if safeState != rng.State {
		panic("rng state was modified unexpectedly")
	}
	workArrayQuick := make([]float64, N)
	fillArray(workArrayQuick, rng)
	if safeState != rng.State {
		panic("rng state was modified unexpectedly")
	}

	// Warm-up both methods
	_ = rtcompare.Median(workArrayMedian)
	_ = rtcompare.QuickMedian(workArrayQuick)

	// Collect timing samples
	var timesMedian []float64
	var timesQuick []float64

	for range repeats {
		rng = rtcompare.DPRNG{State: uint64(rand.Uint64()&0xFFFFFFFFFFFFFFE + 1)} // set rng to a new state for each timing sample

		// Measure Median
		t1 := rtcompare.SampleTime()
		for range innerLoops {
			fillArray(workArrayMedian, rng) // refresh the data in the working array - this function has constant runtime
			_ = rtcompare.Median(workArrayMedian)
		}
		t2 := rtcompare.SampleTime()
		durMedian := float64(rtcompare.DiffTimeStamps(t1, t2)) / float64(innerLoops)
		timesMedian = append(timesMedian, durMedian)

		// Measure QuickMedian (mutates input)
		t3 := rtcompare.SampleTime()
		for range innerLoops {
			fillArray(workArrayQuick, rng) // refresh the data in the working array - this function has constant runtime
			_ = rtcompare.QuickMedian(workArrayQuick)
		}
		t4 := rtcompare.SampleTime()
		durQuick := float64(rtcompare.DiffTimeStamps(t3, t4)) / float64(innerLoops)
		timesQuick = append(timesQuick, durQuick)
	}

	// Compare the timing distributions using bootstrap
	speedups := []float64{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0} // relative speedups to test
	results, err := rtcompare.CompareRuntimes(timesQuick, timesMedian, speedups, precisionLevel)
	if err != nil {
		panic(err)
	}

	// Report results
	fmt.Println("⏱️ Runtime comparison: QuickMedian vs. Median for arrays of size", N)
	for _, r := range results {
		fmt.Printf("Speedup ≥ %.2f%% → Confidence: %.3f%%\n", r.RelativeSpeedupSampleAvsSampleB*100.0, r.Confidence*100.0)
	}
}

// fillArray fills the given array with random float64 values using the provided DPRNG.
// The function modifies the contents of the array in place.
// This function has constant runtime for an array of a fixed size as rtcompare.DPRNG generates values in constant time.
func fillArray(array []float64, rng rtcompare.DPRNG) {
	for i := range array {
		array[i] = rng.Float64()
	}
}
