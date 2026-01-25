[![Go Report Card](https://goreportcard.com/badge/github.com/TomTonic/rtcompare)](https://goreportcard.com/report/github.com/TomTonic/rtcompare)
[![Go Reference](https://pkg.go.dev/badge/github.com/TomTonic/rtcompare.svg)](https://pkg.go.dev/github.com/TomTonic/rtcompare)
[![Linter](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml/badge.svg)](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml)
[![Tests](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml/badge.svg?branch=main)](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml)
![coverage](https://raw.githubusercontent.com/TomTonic/rtcompare/badges/.badges/main/coverage.svg)

<!-- vuln-scan:start -->
<img src="https://user-images.githubusercontent.com/5199289/136855393-d0a9eef9-ccf1-4e2b-9d7c-7aad16a567e5.png" width="16" height="24" alt="grype logo" /> **grype** security scan of Go module [rtcompare v0.5.0](https://github.com/TomTonic/rtcompare/releases/tag/v0.5.0): **0 Vulnerabilities** (0 critical, 0 high, 0 medium, 0 low severity). Used [grype version 0.105.0](https://github.com/anchore/grype/releases/tag/v0.105.0) with DB schema v6.1.3, built 2026-01-25T06:15:59Z.
<!-- vuln-scan:end -->

# rtcompare

## Statistically significant runtime comparison for codepaths in golang

rtcompare is a small, focused Go library for robust runtime or memory measurement comparisons and lightweight benchmarking. It provides utilities to collect timing samples, compare sample distributions using bootstrap techniques, and helper primitives (deterministic PRNG, sample timing helpers, small statistics utilities). The project is intended as a practical alternative to the standard `testing` benchmarking harness when you want reproducible, distribution-aware comparisons and confidence estimates for relative speedups.

Keywords: benchmarking, performance, bootstrap, runtime comparison, statistics, deterministic prng, go

## Features

- Collect per-run timing or memory consumption samples for two implementations and compare their distributions.
- Compute confidence that implementation A is faster or less memory consuming than B by at least a given relative gain using bootstrap resampling.
- Deterministic DPRNG for reproducible input generation.
- Timing helpers (SampleTime, DiffTimeStamps) and small statistics utilities (mean, median, stddev).
- Small, dependency-light API suitable for integration into CI and micro-benchmarks.
- CPRNG — a new cryptographically secure PRNG backed by crypto/rand for scenarios that require cryptographic strength or unpredictable inputs (see API highlights).

## Install

Use as a normal Go module dependency:

```shell
go get github.com/TomTonic/rtcompare
```

Import:

```go
import "github.com/TomTonic/rtcompare"
```

## Quickstart example

This example demonstrates how to collect timing samples for two implementations candidate A and candidate B and compare them (see cmd/rtcompare-example/main.go for a full runnable example).

```go
import (
    "fmt"
    "math/rand"

    "github.com/TomTonic/rtcompare"
)

func example() {
    // generate some timing samples for two functions
    var timesA, timesB []float64
    for i := 0; i < 50; i++ {
        // set up inputs deterministically using DPRNG
        dprng := rtcompare.NewDPRNG()
        // measure repeatedly to reduce quantization noise
        t1 := rtcompare.SampleTime()
        for j := 0; j < 2000; j++ {
            // call candidate A
            // use dprng with constant runtime if necessary
        }
        t2 := rtcompare.SampleTime()
        timesA = append(timesA, float64(rtcompare.DiffTimeStamps(t1, t2))/2000.0)

        // ... same for candidate B ...
    }

    // Compare distributions using bootstrap (precision controls bootstrap repetitions)
    speedups := []float64{0.1, 0.2, 0.5, 1.0} // relative speedups in % to test
    // use the package default resamples or provide a numeric value
    results, err := rtcompare.CompareSamplesDefault(timesA, timesB, speedups)
    if err != nil {
        panic(err)
    }
    for _, r := range results {
        fmt.Printf("Speedup ≥ %.0f%% → Confidence %.2f%%\n", r.RelativeSpeedupSampleAvsSampleB*100, r.Confidence*100)
    }
}
```

## Technical background

- Bootstrap-based inference: Instead of reporting a single sample mean or relying on the `testing` harness, rtcompare collects timing samples across independent runs and uses bootstrap resampling to estimate the confidence that one implementation is faster than another by at least a given relative margin. This yields more informative, distribution-aware results (confidence intervals and probability estimates).
- Deterministic input generation: DPRNG is provided to seed and generate reproducible inputs across runs, helping reduce input variance when comparing implementations. For cases that require cryptographic strength or unpredictable inputs (for example, testing code that must handle cryptographic-quality randomness), rtcompare now provides CPRNG, a [crypto/rand](https://pkg.go.dev/crypto/rand)-backed PRNG. Use DPRNG when you need deterministic, repeatable, extremely fast inputs; use CPRNG when you need cryptographic unpredictability or higher entropy.

- Noise reduction: The example shows how to warm up, use multiple inner iterations per timing sample to reduce quantization noise, and manually trigger GC cycles to reduce interference from allocations.

## When to use rtcompare instead of `testing.B`

Use rtcompare when you want:

- Distribution-aware comparisons rather than single-number reports.
- Statistical confidence estimates for relative speedups (e.g., "A is at least 20% faster than B with 95% confidence").
- A library you can easily call from small programs, CI jobs, or dedicated comparison tools without the `testing` harness.

The standard `testing` package is excellent for microbenchmarks and tight per-op measurements. rtcompare complements it by focusing on sampling strategy, resampling inference, and reproducible comparisons across implementations.

## API highlights

- DPRNG — deterministic PRNG with Uint64 and Float64 helpers.
- CPRNG — cryptographically secure PRNG backed by crypto/rand. Provides the same convenience helpers (Uint64, Float64) as DPRNG but yields cryptographic-strength randomness; not deterministic across runs.
- SampleTime() / DiffTimeStamps() — helpers for high-resolution timing.
- CompareSamples(timesA, timesB, speedups, resamples) — returns confidence estimates per requested relative speedup. Use `rtcompare.DefaultResamples` or the convenience wrapper `rtcompare.CompareSamplesDefault` for a sensible default.
- QuickMedian — returns the median of a Float64 slice in expected O(n) time.

Note on negative `relativeGains`: Negative thresholds are allowed and are
interpreted as tolerated relative slowdowns rather than speedups. A threshold
of `-0.05` means "A is within 5% of B" (i.e., A is not more than 5% slower
than B). Use negative values when you want to ask whether one implementation
is approximately as fast as another within a relative tolerance instead of
requiring a strict speedup.

### Choosing `resamples`

The number of bootstrap resamples controls the Monte‑Carlo error of the confidence estimates. Common recommendations from the bootstrap literature (Efron & Tibshirani; Davison & Hinkley) are:

- Use at least 1,000 resamples for reasonable standard-error estimation.
- Use 5,000–10,000 resamples when estimating percentile confidence intervals or when you need stability in tails.

The Monte‑Carlo standard error of a proportion estimated from resamples decreases approximately as 1/sqrt(R) where R is the number of resamples. Increase `resamples` when you require low Monte‑Carlo noise (for example, precise reporting of extreme thresholds). See Efron & Tibshirani (1993) and Davison & Hinkley (1997) for more details.

See the package docs and the example in `cmd/rtcompare-example` for detailed usage.

## Contributing

Contributions welcome. Please open issues or PRs for bug reports, performance tweaks, or additional comparison strategies. Add tests for any behavioral changes and keep CPU/memory overheads small.

## License

MIT — see LICENSE file.
