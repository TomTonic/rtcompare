package rtcompare

import (
	"fmt"
	"math/rand"
	"slices"
)

type RTcomparisonResult struct {
	RelativeSpeedupSampleAvsSampleB float64
	Confidence                      float64
}

const MinimumDataPoints uint64 = 11

// CompareRuntimes compares two samples of runtimes (in float64, e.g., milliseconds)
// and computes the confidence that sample A is faster than sample B by at least
// the specified relative speedups. The precisionLevel parameter controls the number of bootstrap
// repetitions (higher values yield more precise results of the statistical tests but take longer to compute).
// It returns a slice of RTcomparisonResult, each containing one of the given relative speedup and
// the corresponding confidence level calculated by the statistical test.
// If there are not enough data points in either sample, an error is returned.
func CompareRuntimes(sampleA, sampleB []float64, relativeSpeedupsToTest []float64, precisionLevel uint64) (result []RTcomparisonResult, err error) {
	if uint64(len(sampleA)) < MinimumDataPoints || uint64(len(sampleB)) < MinimumDataPoints {
		return []RTcomparisonResult{}, fmt.Errorf("not enough data points: need at least 11 runtimes for each of A and B")
	}
	if len(relativeSpeedupsToTest) == 0 {
		relativeSpeedupsToTest = []float64{0.0}
	}

	slices.Sort(relativeSpeedupsToTest)

	conf := bootstrapConfidence(sampleA, sampleB, relativeSpeedupsToTest, precisionLevel, 0)

	for _, t := range relativeSpeedupsToTest {
		r := RTcomparisonResult{
			RelativeSpeedupSampleAvsSampleB: t,
			Confidence:                      conf[t],
		}
		result = append(result, r)
	}
	return result, nil
}

func bootstrapSample(xs []float64, prngSeed uint64) []float64 {
	if prngSeed == 0 {
		prngSeed = uint64(rand.Uint64()&0xFFFFFFFFFFFFFFE + 1) // avoid zero seed
	}
	rng := DPRNG{State: prngSeed}

	n := len(xs)
	sample := make([]float64, n)
	for i := range n {
		sample[i] = xs[rng.Uint64()%uint64(n)]
	}
	return sample
}

func bootstrapConfidence(A, B []float64, thresholds []float64, reps uint64, prngSeed uint64) map[float64]float64 {
	conf := make(map[float64]float64, len(thresholds))
	counts := make(map[float64]int, len(thresholds))

	for range reps {
		sampleA := bootstrapSample(A, prngSeed)
		sampleB := bootstrapSample(B, prngSeed)
		delta := 1 - QuickMedian(sampleA)/QuickMedian(sampleB) // relative speedup

		for _, threshold := range thresholds {
			if delta >= threshold {
				counts[threshold]++
			}
		}
	}

	for _, threshold := range thresholds {
		conf[threshold] = float64(counts[threshold]) / float64(reps)
	}
	return conf
}
