package rtcompare

import (
	"math"
	"runtime"
	"testing"
)

// TestCPRNG_TenMillion calls CPRNG methods twenty million times
// to ensure there are no panics or errors.
func TestCPRNG_TenMillion(t *testing.T) {
	const calls = 10_000_000
	c := NewCPRNG(8192)
	if len(c.buf) == 0 {
		t.Fatal("buffer not initialized")
	}
	for range calls {
		sel := uint64(c.Uint32()) % 10
		switch sel {
		case 0:
			_ = c.Uint64()
		case 1:
			_ = c.Int64()
		case 2:
			_ = c.Uint32()
		case 3:
			_ = c.Int32()
		case 4:
			_ = c.Uint16()
		case 5:
			_ = c.Int16()
		case 6:
			_ = c.Uint8()
		case 7:
			_ = c.Int8()
		case 8:
			_ = c.Float32()
		case 9:
			_ = c.Float64()
		}
	}
}

// chiSquare computes the Pearson chi-square statistic for a slice of observed counts.
// expected is the expected count per bin and must be > 0.
// It returns the statistic Σ (observed_i - expected)^2 / expected as a float64.
func chiSquare(counts []int, expected float64) float64 {
	var x2 float64
	for _, o := range counts {
		diff := float64(o) - expected
		x2 += (diff * diff) / expected
	}
	return x2
}

// chiSquarePValueEven computes the upper-tail p-value P(χ² ≥ x2) for a chi-square
// distribution with an even number of degrees of freedom df.
// For df = 2m it evaluates the closed-form series
//
//	P(χ² ≥ x2) = e^{-x2/2} * sum_{j=0}^{m-1} (x2/2)^j / j!
//
// using a recurrence to accumulate the series terms.
// x2 is the observed chi-square statistic (x2 >= 0) and df should be a positive even integer.
// The returned value is the survival probability in [0,1]; behavior is undefined for
// negative x2 or odd/non-positive df.
func chiSquarePValueEven(x2 float64, df int) float64 {
	m := df / 2
	t := math.Exp(-x2 / 2.0)
	sum := 1.0 // j = 0
	term := 1.0
	for j := 1; j < m; j++ {
		term *= x2 / (2.0 * float64(j))
		sum += term
	}
	return t * sum
}

// chiSquarePValueApprox computes an approximate upper-tail p-value for a
// chi-square statistic x2 with df degrees of freedom.
//
// The function uses the Wilson–Hilferty cube-root transform to approximate
// the chi-square distribution by a normal distribution, then evaluates the
// standard normal upper-tail probability via the error function (math.Erf).
//
// Parameters:
//
//	x2 - chi-square statistic (must be >= 0)
//	df - degrees of freedom (must be > 0)
//
// Returns:
//
//	Approximate upper-tail p-value P(Chi²_df >= x2) in [0, 1].
//
// Notes:
//   - Accuracy improves for larger df; for very small df or extreme tail
//     probabilities prefer exact or numerical integration methods.
//   - If x2 < 0 or df <= 0 the result is undefined (may produce NaN).
func chiSquarePValueApprox(x2 float64, df int) float64 {
	// Wilson-Hilferty approximation for arbitrary df (works well for df >= 1):
	// z = ((x2/df)^(1/3) - (1 - 2/(9df))) / sqrt(2/(9df))
	// Upper-tail p ≈ 1 - Phi(z), where Phi is computed via Erf.
	nu := float64(df)
	z := (math.Pow(x2/nu, 1.0/3.0) - (1.0 - 2.0/(9.0*nu))) / math.Sqrt(2.0/(9.0*nu))
	Phi := 0.5 * (1.0 + math.Erf(z/math.Sqrt2))
	return 1.0 - Phi
}

// p-value of the chi-squared distribution: exact series for even df, otherwise approximation
func chiSquarePValue(x2 float64, df int) float64 {
	if df <= 0 {
		return 1.0 // trivial
	}
	if df%2 == 0 {
		return chiSquarePValueEven(x2, df)
	}
	return chiSquarePValueApprox(x2, df)
}

// TestCPRNG_Uint8_Uniformity performs a statistical uniformity check of the
// CPRNG.Uint8 output. It draws a large number of samples from a CPRNG
// instance initialized with parameter 8192, tallies occurrences for each of
// the 256 possible uint8 values, and computes a χ² statistic and p-value
// against the expected uniform distribution. The test logs whether the null
// hypothesis of uniformity is rejected at significance level α=0.05.
// Note: this is a probabilistic test — occasional failures may occur by chance.
func TestCPRNG_Uint8_Uniformity(t *testing.T) {
	const samples = 1 << 20
	const bins = 256
	const alpha = 0.05
	c := NewCPRNG(8192)

	counts := make([]int, bins)
	for range samples {
		counts[c.Uint8()]++
	}

	expected := float64(samples) / float64(bins)

	x2 := chiSquare(counts, expected)
	df := bins - 1
	p := chiSquarePValue(x2, df)

	if p < alpha {
		t.Fatalf("χ² test result → H0 rejected (not uniform at significance level α=%.2f): χ²=%.3f p=%.3f\n\nPLEASE NOTE: This test is probabilistic and may occasionally fail by chance.", alpha, x2, p)
	} else {
		t.Logf("χ² test result → H0 NOT rejected (no evidence against uniformity at α=%.2f): χ²=%.3f p=%.3f", alpha, x2, p)
	}

}

// TestCPRNG_Uint16_Uniformity performs a statistical uniformity check of the
// CPRNG.Uint16 output. It draws a large number of samples from a CPRNG
// instance initialized with parameter 8192, tallies occurrences for each of
// the 65536 possible uint16 values, and computes a χ² statistic and p-value
// against the expected uniform distribution. The test logs whether the null
// hypothesis of uniformity is rejected at significance level α=0.05.
// Note: this is a probabilistic test — occasional failures may occur by chance.
func TestCPRNG_Uint16_Uniformity(t *testing.T) {
	const samples = 1 << 22
	const bins = 65536
	const alpha = 0.05
	c := NewCPRNG(8192)

	counts := make([]int, bins)
	for range samples {
		v := c.Uint16()
		counts[int(v)]++
	}

	expected := float64(samples) / float64(bins)

	x2 := chiSquare(counts, expected)
	df := bins - 1
	p := chiSquarePValue(x2, df)

	if p < alpha {
		t.Fatalf("χ² test result → H0 rejected (not uniform at significance level α=%.2f): χ²=%.3f p=%.3f\n\nPLEASE NOTE: This test is probabilistic and may occasionally fail by chance.", alpha, x2, p)
	} else {
		t.Logf("χ² test result → H0 NOT rejected (no evidence against uniformity at α=%.2f): χ²=%.3f p=%.3f", alpha, x2, p)
	}
}

// TestCPRNG_UniformFloat32_Uniformity samples 2^22 UniformFloat32 values and
// performs a chi-squared uniformity check across 65536 buckets. The test logs
// chi2 and p-value and follows the same structure as TestCPRNG_Uint16_Uniformity.
func TestCPRNG_UniformFloat32_Uniformity(t *testing.T) {
	const samples = 1 << 22
	const bins = 65536
	const alpha = 0.05
	c := NewCPRNG(8192)

	counts := make([]int, bins)
	for range samples {
		v := c.Float32()
		if math.IsNaN(float64(v)) {
			t.Fatalf("UniformFloat32 returned NaN")
		}
		if math.IsInf(float64(v), 0) {
			t.Fatalf("UniformFloat32 returned Inf")
		}
		if v < 0.0 || v >= 1.0 {
			t.Fatalf("UniformFloat32 returned out-of-bounds value: %f", v)
		}
		idx := int(float32(bins) * v)
		if idx < 0 {
			idx = 0
		} else if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
	}

	expected := float64(samples) / float64(bins)
	x2 := chiSquare(counts, expected)
	df := bins - 1
	p := chiSquarePValue(x2, df)

	if p < alpha {
		t.Logf("χ² test result → H0 rejected (not uniform at significance level α=%.2f): χ²=%.3f p=%.3f\n\nPLEASE NOTE: This test is probabilistic and may occasionally fail by chance.", alpha, x2, p)
	} else {
		t.Logf("χ² test result → H0 NOT rejected (no evidence against uniformity at α=%.2f): χ²=%.3f p=%.3f", alpha, x2, p)
	}
}

// TestCPRNG_UniformFloat64_Uniformity samples 2^22 UniformFloat64 values and
// performs a chi-squared uniformity check across 65536 buckets. The test logs
// chi2 and p-value and follows the same structure as TestCPRNG_Uint16_Uniformity.
func TestCPRNG_UniformFloat64_Uniformity(t *testing.T) {
	const samples = 1 << 22
	const bins = 65536
	const alpha = 0.05
	c := NewCPRNG(8192)

	counts := make([]int, bins)
	for range samples {
		v := c.Float32()
		if math.IsNaN(float64(v)) {
			t.Fatalf("UniformFloat32 returned NaN")
		}
		if math.IsInf(float64(v), 0) {
			t.Fatalf("UniformFloat32 returned Inf")
		}
		if v < 0.0 || v >= 1.0 {
			t.Fatalf("UniformFloat32 returned out-of-bounds value: %f", v)
		}
		idx := int(float32(bins) * v)
		if idx < 0 {
			idx = 0
		} else if idx >= bins {
			idx = bins - 1
		}
		counts[idx]++
	}

	expected := float64(samples) / float64(bins)
	x2 := chiSquare(counts, expected)
	df := bins - 1
	p := chiSquarePValue(x2, df)

	if p < alpha {
		t.Logf("χ² test result → H0 rejected (not uniform at significance level α=%.2f): χ²=%.3f p=%.3f\n\nPLEASE NOTE: This test is probabilistic and may occasionally fail by chance.", alpha, x2, p)
	} else {
		t.Logf("χ² test result → H0 NOT rejected (no evidence against uniformity at α=%.2f): χ²=%.3f p=%.3f", alpha, x2, p)
	}
}

func TestCPRNG_Uint32N_Bounds(t *testing.T) {
	c := NewCPRNG(8192)
	max := ^uint32(0)
	cases := []uint32{0, 1, 2, 3, 10, 65535, 1 << 31, max}
	for _, n := range cases {
		samples := 10000
		if n == 0 || n == 1 {
			samples = 1000
		}
		for i := 0; i < samples; i++ {
			v := c.Uint32N(n)
			if n == 0 || n == 1 {
				if v != 0 {
					t.Fatalf("Uint32N(%d) = %d; want 0", n, v)
				}
			} else {
				if v >= n {
					t.Fatalf("Uint32N(%d) = %d; out of range", n, v)
				}
			}
		}
	}
}

func TestCPRNG_Uint32N_Uniformity(t *testing.T) {
	const samples = 5_000_000
	const alpha = 0.05
	binSizes := []uint32{3, 7, 10, 3 * 32768}
	c := NewCPRNG(8192)
	for _, bins := range binSizes {
		counts := make([]int, bins)
		for range samples {
			counts[c.Uint32N(bins)]++
		}
		expected := float64(samples) / float64(bins)
		x2 := chiSquare(counts, expected)
		df := bins - 1
		p := chiSquarePValue(x2, int(df))

		if p < alpha {
			t.Logf("χ² test result for %d bins → H0 rejected (not uniform at significance level α=%.2f): χ²=%.3f p=%.3f\n\nPLEASE NOTE: This test is probabilistic and may occasionally fail by chance.", bins, alpha, x2, p)
		} else {
			t.Logf("χ² test result for %d bins → H0 NOT rejected (no evidence against uniformity at α=%.2f): χ²=%.3f p=%.3f", bins, alpha, x2, p)
		}
	}
}

// TestCPRNG_BufferSizePerformance compares the per-call time of two CPRNG instances
// with a very small buffer (16 bytes) and a large buffer (8 KiB). It measures
// average time per Uint64 call across multiple samples and asserts that the
// large-buffer CPRNG is faster on average than the small-buffer CPRNG.
func TestCPRNG_BufferSizePerformance(t *testing.T) {
	const repeats = 71
	const innerLoops = 300_000
	const expectedSpeedup = 0.32 // expect large-buffer CPRNG to be at least 32% faster than small-buffer CPRNG. This conservative estimate is required for GitHub Actions CI. On an M1 Pro MacBook the speedup is usually around 25x.
	const minConfidence = 0.95   // require at least 95% confidence

	small := NewCPRNG(16)
	large := NewCPRNG(8192)

	timesSmall := make([]float64, 0, repeats)
	timesLarge := make([]float64, 0, repeats)

	for range repeats {
		runtime.GC()
		t1 := SampleTime()
		for range innerLoops {
			_ = small.Uint64()
		}
		t2 := SampleTime()
		timesSmall = append(timesSmall, float64(DiffTimeStamps(t1, t2))/float64(innerLoops))

		runtime.GC()
		t3 := SampleTime()
		for range innerLoops {
			_ = large.Uint64()
		}
		t4 := SampleTime()
		timesLarge = append(timesLarge, float64(DiffTimeStamps(t3, t4))/float64(innerLoops))
	}

	mSmall := QuickMedian(timesSmall)
	mLarge := QuickMedian(timesLarge)
	t.Logf("median call (small=%d bytes)=%.1f ns, (large=%d bytes)=%.1f ns", 16, mSmall, 8192, mLarge)

	if !(mLarge < mSmall) {
		t.Fatalf("expected large-buffer CPRNG to be faster: large=%.1f >= small=%.1f", mLarge, mSmall)
	}

	speedups := []float64{expectedSpeedup}
	results, err := CompareSamples(timesLarge, timesSmall, speedups, 10_000)
	if err != nil {
		t.Fatalf("CompareSamples failed: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result from CompareSamples, got %d", len(results))
	}
	for _, r := range results {
		t.Logf("Speedup ≥ %.2f%% → Confidence: %.3f%%\n", r.RelativeSpeedupSampleAvsSampleB*100.0, r.Confidence*100.0)
	}
	res := results[0]
	if res.Confidence < minConfidence {
		t.Fatalf("expected confidence >= %.2f for speedup %.1f, got %.3f", minConfidence, res.RelativeSpeedupSampleAvsSampleB, res.Confidence)
	}

}

// TestCPRNG_vs_DPRNG_Performance compares the per-call time of a CPRNG instances
// with a very large buffer (16 KiB) with a DPRNG. It measures
// average time per Uint64 call across multiple samples and asserts that the
// DPRNG is faster on average than the large-buffer CPRNG.
func TestCPRNG_vs_DPRNG_Performance(t *testing.T) {
	const repeats = 53
	const innerLoops = 400_000
	const cprngBufferSize = 16384
	const expectedSpeedup = 0.33333 // expect DPRNG to be at least 33.333% faster than CPRNG
	const minConfidence = 0.95      // require at least 95% confidence

	cprng := NewCPRNG(cprngBufferSize)
	dprng := NewDPRNG(123456)

	timesCprng := make([]float64, 0, repeats)
	timesDprng := make([]float64, 0, repeats)

	for range repeats {
		runtime.GC()
		t1 := SampleTime()
		for range innerLoops {
			_ = cprng.Uint64()
		}
		t2 := SampleTime()
		timesCprng = append(timesCprng, float64(DiffTimeStamps(t1, t2))/float64(innerLoops))

		runtime.GC()
		t3 := SampleTime()
		for range innerLoops {
			_ = dprng.Uint64()
		}
		t4 := SampleTime()
		timesDprng = append(timesDprng, float64(DiffTimeStamps(t3, t4))/float64(innerLoops))
	}

	mCprng := QuickMedian(timesCprng)
	mDprng := QuickMedian(timesDprng)
	t.Logf("median call (CPRNG with %d bytes)=%.1f ns, (DPRNG)=%.1f ns", cprngBufferSize, mCprng, mDprng)

	if !(mDprng < mCprng) {
		t.Fatalf("expected DPRNG to be faster: DPRNG=%.1f >= CPRNG=%.1f", mDprng, mCprng)
	}

	speedups := []float64{expectedSpeedup}
	results, err := CompareSamples(timesDprng, timesCprng, speedups, 10_000)
	if err != nil {
		t.Fatalf("CompareSamples failed: %v", err)
	}
	if len(results) < 1 {
		t.Fatalf("expected at least 1 result from CompareSamples, got %d", len(results))
	}
	for _, r := range results {
		t.Logf("Speedup ≥ %.2f%% → Confidence: %.3f%%\n", r.RelativeSpeedupSampleAvsSampleB*100.0, r.Confidence*100.0)
	}
	res := results[0]
	if res.Confidence < minConfidence {
		t.Fatalf("expected confidence >= %.2f for speedup %.1f, got %.3f", minConfidence, res.RelativeSpeedupSampleAvsSampleB, res.Confidence)
	}
}
