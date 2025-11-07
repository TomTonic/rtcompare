package rtcompare

import (
	"math"
	"slices"
)

// Median computes the median of the provided slice of float64.
// If data is empty, Median returns 0.0.
// The function makes a copy of the input and sorts the copy, so the original slice is not modified.
// For an odd-length slice it returns the middle element; for an even-length slice it returns
// the element at index len(data)/2 (the upper middle).
// Time complexity: O(n log n). Space complexity: O(n) due to the copy required for sorting.
func Median(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	dataCopy := make([]float64, len(data))
	copy(dataCopy, data)
	slices.Sort(dataCopy)

	l := len(dataCopy)
	return dataCopy[l/2]
}

// Statistics computes the arithmetic mean, population variance, and standard deviation
// of the provided slice of float64 values.
//
// It returns three float64s in order: mean, variance, and stddev.
//
// The variance returned is the population variance (sum of squared deviations divided by n).
// The standard deviation is the square root of that variance.
//
// If the input slice is empty, the function returns mean = 0 and variance = stddev = -1
// to indicate that the values are undefined for an empty dataset.
func Statistics(data []float64) (mean, variance, stddev float64) {
	if len(data) == 0 {
		return 0, -1, -1
	}

	var sum float64
	n := float64(len(data))

	for _, value := range data {
		sum += value
	}
	mean = sum / n

	for _, value := range data {
		variance += (value - mean) * (value - mean)
	}
	variance /= n
	stddev = math.Sqrt(variance)
	return
}

// FloatsEqualWithTolerance reports whether f1 and f2 are approximately equal,
// using a percentage-based absolute tolerance computed from each operand.
//
// The tolerancePercentage parameter is interpreted as a percentage (for example,
// 5 means 5%). For each value v the function computes
//
//	absTol = |v * tolerancePercentage / 100|
//
// and checks whether the other value lies within [v - absTol, v + absTol].
// The function returns true if either check succeeds (i.e. if f2 is within the
// tolerance computed from f1 OR f1 is within the tolerance computed from f2).
//
// Important notes:
//   - The comparison uses absolute tolerances derived from the operands, which
//     makes the check effectively two-sided: a small value may be considered
//     equal to a much larger one if the larger value's tolerance range contains
//     the smaller value.
//   - A tolerancePercentage of 0 requires exact equality.
//   - Negative tolerancePercentage values are treated equivalently to their
//     absolute value because the computed tolerance is wrapped with math.Abs.
//   - Comparisons involving NaN follow IEEE754 semantics and will not be true;
//     interactions with ±Inf follow IEEE754 and may produce true results when a
//     computed tolerance range is infinite.
//   - This function performs simple arithmetic checks and returns a boolean.
func FloatsEqualWithTolerance(f1, f2, tolerancePercentage float64) bool {
	absTol1 := math.Abs(f1 * tolerancePercentage / 100)
	if f1-absTol1 <= f2 && f1+absTol1 >= f2 {
		return true
	}
	absTol2 := math.Abs(f2 * tolerancePercentage / 100)
	if f2-absTol2 <= f1 && f2+absTol2 >= f1 {
		return true
	}
	return false
}

// Partition rearranges xs around a pivot and returns its final index
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
	rng := NewDPRNG()
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

// QuickMedian returns the median in expected O(n) time.
// In case of an odd number of elements, it returns the middle one.
// In case of an even number of elements, it returns the higher of the two middle ones.
// Returns math.NaN() for an empty input slice.
// Note: This function modifies the input array. To avoid this, pass a copy.
func QuickMedian(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	n := uint64(len(xs))
	median := quickselect(xs, n/2)
	return median
}
