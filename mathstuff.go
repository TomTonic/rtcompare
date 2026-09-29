package rtcompare

import (
	"math"
	"math/rand/v2"
	"slices"

	"github.com/TomTonic/rtcompare/prng"
)

// Median computes the median of the provided slice of float64.
// If data is empty, Median returns NaN, since an empty set has no median.
// The function makes a copy of the input and sorts the copy, so the original slice is not modified.
// For an odd-length slice it returns the middle element; for an even-length
// slice it returns the mean of the two middle elements. Returning either one of
// them instead, as this function used to, biases the median of an even count
// towards that side.
// Time complexity: O(n log n). Space complexity: O(n) due to the copy required for sorting.
func Median(data []float64) float64 {
	if len(data) == 0 {
		return math.NaN()
	}
	dataCopy := make([]float64, len(data))
	copy(dataCopy, data)
	slices.Sort(dataCopy)

	l := len(dataCopy)
	if l%2 == 0 {
		return (dataCopy[l/2-1] + dataCopy[l/2]) / 2
	}
	return dataCopy[l/2]
}

// partition rearranges xs around a pivot and returns its final index.
func partition(xs []float64, low, high uint64) uint64 {
	pivot := xs[high]
	i := low
	for j := low; j < high; j++ {
		if xs[j] < pivot {
			xs[i], xs[j] = xs[j], xs[i]
			i++
		}
	}
	xs[i], xs[high] = xs[high], xs[i]
	return i
}

// quickselect finds the k-th smallest element (0-based index) in expected O(n) time.
// For k = len(xs)/2, it returns the median.
// see https://en.wikipedia.org/wiki/Quickselect
//
// Note: If the input slice is empty or k is out of range the function returns math.NaN().
func quickselect(xs []float64, k uint64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	if k >= uint64(len(xs)) {
		return math.NaN()
	}
	// Random pivots guard against inputs that happen to be ordered; the
	// generator only picks positions, so a fast non-deterministic seed will do.
	rng := prng.NewDPRNG(rand.Uint64())
	low, high := uint64(0), uint64(len(xs)-1)
	for low <= high {
		pivotIndex := rng.Uint64()%(high-low+1) + low
		xs[pivotIndex], xs[high] = xs[high], xs[pivotIndex] // move pivot to end
		p := partition(xs, low, high)
		if p == k {
			return xs[p]
		} else if p < k {
			low = p + 1
		} else {
			high = p - 1
		}
	}
	return xs[k] // fallback
}

// quickMedian returns the median in expected O(n) time. It exists for the
// bootstrap, which takes two medians per replicate of slices it owns.
// In case of an odd number of elements, it returns the middle one.
// In case of an even number of elements, it returns the mean of the two middle ones.
// Returns math.NaN() for an empty input slice.
// Note: This function modifies the input array. To avoid this, pass a copy.
func quickMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	n := uint64(len(xs))
	upper := quickselect(xs, n/2)
	if n%2 == 1 {
		return upper
	}
	// quickselect leaves every element below position n/2 no larger than the
	// one it returned, so the lower middle is the largest of them.
	return (slices.Max(xs[:n/2]) + upper) / 2
}
