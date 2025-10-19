# rtcompare

[![Go Report Card](https://goreportcard.com/badge/github.com/TomTonic/rtcompare)](https://goreportcard.com/report/github.com/TomTonic/rtcompare)
[![Go Reference](https://pkg.go.dev/badge/github.com/TomTonic/rtcompare.svg)](https://pkg.go.dev/github.com/TomTonic/rtcompare)
[![Tests](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml/badge.svg?branch=main)](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml)
![coverage](https://raw.githubusercontent.com/TomTonic/rtcompare/badges/.badges/main/coverage.svg)

## Statistically significant runtime comparison for codepaths in golang

rtcompare is a small, focused Go library for robust runtime comparisons and lightweight benchmarking. It provides utilities to collect timing samples, compare runtime distributions using bootstrap techniques, and helper primitives (deterministic PRNG, sample timing helpers, small statistics utilities). The project is intended as a practical alternative to the standard `testing` benchmarking harness when you want reproducible, distribution-aware comparisons and confidence estimates for relative speedups.

Keywords: benchmarking, performance, bootstrap, runtime comparison, statistics, deterministic prng, go

## Features

- Collect per-run timing samples for two implementations and compare their runtime distributions.
- Compute confidence that implementation A is faster than B by at least a given relative speedup using bootstrap resampling.
- Deterministic DPRNG for reproducible input generation.
- Timing helpers (SampleTime, DiffTimeStamps) and small statistics utilities (mean, median, stddev).
- Small, dependency-light API suitable for integration into CI and micro-benchmarks.

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

This example demonstrates how to collect timing samples for two median implementations and compare them (see cmd/rtcompare-example/main.go for a full runnable example).

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
        dprng := rtcompare.DPRNG{State: uint64(rand.Uint64()&0xFFFFFFFFFFFFFFE + 1)}
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
    speedups := []float64{0.1, 0.2, 0.5, 1.0} // relative speedups to test
    results, err := rtcompare.CompareRuntimes(timesA, timesB, speedups, 10000)
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
- Deterministic input generation: DPRNG is provided to seed and generate reproducible inputs across runs, helping reduce input variance when comparing implementations.
- Noise reduction: The example shows how to warm up, use multiple inner iterations per timing sample to reduce quantization noise, and manually trigger GC cycles to reduce interference from allocations.

## When to use rtcompare instead of `testing.B`

Use rtcompare when you want:

- Distribution-aware comparisons rather than single-number reports.
- Statistical confidence estimates for relative speedups (e.g., "A is at least 20% faster than B with 95% confidence").
- A library you can easily call from small programs, CI jobs, or dedicated comparison tools without the `testing` harness.

The standard `testing` package is excellent for microbenchmarks and tight per-op measurements. rtcompare complements it by focusing on sampling strategy, resampling inference, and reproducible comparisons across implementations.

## API highlights

- DPRNG — deterministic PRNG with Uint64 and Float64 helpers.
- SampleTime() / DiffTimeStamps() — helpers for high-resolution timing.
- CompareRuntimes(timesA, timesB, speedups, precision) — returns confidence estimates per requested relative speedup.

See the package docs and the example in cmd/rtcompare-example for detailed usage.

## Contributing

Contributions welcome. Please open issues or PRs for bug reports, performance tweaks, or additional comparison strategies. Add tests for any behavioral changes and keep CPU/memory overheads small.

## License

MIT — see LICENSE file.
