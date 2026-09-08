# rtcompare

[![Go Report Card](https://goreportcard.com/badge/github.com/TomTonic/rtcompare)](https://goreportcard.com/report/github.com/TomTonic/rtcompare)
[![Go Reference](https://pkg.go.dev/badge/github.com/TomTonic/rtcompare.svg)](https://pkg.go.dev/github.com/TomTonic/rtcompare)
[![Linter](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml/badge.svg)](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml)
[![Tests](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml/badge.svg?branch=main)](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml)
![coverage](https://raw.githubusercontent.com/TomTonic/rtcompare/badges/.badges/main/coverage.svg)
[![Vulnerabilities](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db/raw/release_scan.json)](https://gist.github.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db#file-release_scan-md)

## Statistically significant runtime comparison for codepaths in golang

rtcompare is a small Go library for deciding whether one code path is genuinely faster than another. It measures both candidates, resamples the measurements to estimate how confident that conclusion is, and — the part that distinguishes it — measures what the machine invents on its own so that the conclusion can be read against it.

Keywords: benchmarking, performance, bootstrap, runtime comparison, statistics, deterministic prng, go

## Features

- Collect timing or memory samples for two implementations under a harness that interleaves their measurement order, keeps setup out of the measured region, and places garbage collection deterministically.
- Size batches automatically, so that the system clock contributes at most a chosen share of error. This is what makes differences far below the clock's resolution measurable: a per-operation difference of 1.89 ns was recovered to within 0.07 percentage points against a 41 ns clock floor.
- Estimate, by bootstrap resampling, the confidence that A beats B by at least a given relative margin.
- Measure the harness against itself, so that a result can be compared with the difference the same setup reports between two runs of identical code.
- Detect a trend across a measurement run, which resampling structurally cannot see because it discards the order the samples arrived in.
- Resample in blocks when the measurements are correlated enough that treating them as independent would overstate confidence.
- Deterministic PRNG for reproducible inputs, and a crypto/rand-backed one where unpredictability is wanted.

## What this cannot tell you

Two limits are worth knowing before the first run, because neither is visible in a confidence figure.

**Attenuation.** What is measured is the loop, not the function. Whatever fixed per-operation cost the batch body carries — the loop itself, an accumulator, regenerating an input the candidate mutates — is present in both candidates and shrinks the difference between them. In a controlled experiment where the true difference was exactly 50%, the measured difference was 35%, because 1.81 ns/op of loop overhead sat on top of 2.13 ns/op of real work. Subtracting an empty-loop baseline does not repair it: the compiler optimizes an empty loop differently, and that correction recovered 2 of the 15 missing percentage points. Read a result as the speedup of the measured region, not of the isolated function.

**The noise floor.** Resampling quantifies how much an estimate would move if the same measurements were drawn again. It cannot see a bias that affected all of them equally, and will report a tight confidence around one. Measured on identical code, this package has seen apparent differences from a few tenths of a percent to well over one, carried with high confidence. `ValidateHarness` exists to measure that floor for your machine and your options; a result below it has resolved nothing, however confident the number looks.

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

```go
package main

import (
	"fmt"

	"github.com/TomTonic/rtcompare"
)

var sink float64

func main() {
	candidateA := rtcompare.Candidate{Name: "A", Batch: func(n uint64) {
		var acc float64
		for range n {
			acc += doSomething()
		}
		sink += acc
	}}
	candidateB := rtcompare.Candidate{Name: "B", Batch: func(n uint64) { /* ... */ }}

	// InnerLoops left at zero: Collect sizes the batches itself.
	opts := rtcompare.CollectOptions{GCBetween: true}

	// Ask what this machine invents on its own before asking what the
	// candidates differ by.
	v, err := rtcompare.ValidateHarness(candidateA,
		rtcompare.ValidationOptions{Collect: opts, Runs: 10})
	if err != nil {
		panic(err)
	}
	fmt.Println(v)

	samplesA, samplesB, err := rtcompare.Collect(candidateA, candidateB, opts)
	if err != nil {
		panic(err)
	}

	observed := 1 - rtcompare.Median(samplesA)/rtcompare.Median(samplesB)
	if !v.Resolves(observed) {
		fmt.Printf("%.2f%% is inside the %.2f%% noise floor; nothing resolved\n",
			observed*100, v.NoiseFloor*100)
		return
	}

	results, err := rtcompare.CompareSamplesDefault(samplesA, samplesB,
		[]float64{v.NoiseFloor, 0.05, 0.10, 0.20})
	if err != nil {
		panic(err)
	}
	for _, r := range results {
		fmt.Printf("Speedup >= %.2f%% -> confidence %.2f%%\n",
			r.RelativeSpeedupSampleAvsSampleB*100, r.Confidence*100)
	}
}
```

See `cmd/rtcompare-example` for a full runnable version that compares this package's own two median implementations, and that finds one of them badly enough behaved to need block resampling.

## Technical background

- **Batching is what beats the clock.** A single batch measurement is off by at most one clock tick `p`. Spread over `n` operations that is `p/n` per operation, so the relative error is `p/(n·c)` where `c` is one operation's cost. Since `n·c` is just the batch duration `T`, the whole thing collapses to `p/T`: the error depends only on how long a batch runs, not on how fast the operation is. `CalibrateInnerLoops` therefore searches for the smallest batch that reaches a target duration, which is why an expensive operation can calibrate to a batch of two while a cheap one needs thirteen thousand.

- **Bootstrap-based inference.** Rather than a single mean, rtcompare resamples the collected measurements to estimate the probability that one implementation beats the other by at least a given relative margin. Note that it reports those probabilities, not confidence intervals around the difference itself.

- **What resampling cannot see.** The bootstrap treats the samples as an unordered bag, which discards the order they were measured in. A machine that drifted during the run leaves no trace in its output. `DetectDrift` tests for that separately, using Spearman's rank correlation against measurement position; its false positive rate was verified at 4.80% against a nominal 5% over 6000 permutations of real measurement series.

- **Dependence between neighbouring measurements.** Resampling single observations also assumes they are exchangeable, and real measurements are mildly correlated. In AR(1) simulations the rate of false signals from identical inputs stayed at its nominal 10% up to a lag-1 correlation of 0.08, reached 13.5% at 0.2 and 21.7% at 0.4. `ValidateHarness` reports the correlation it observed; above roughly 0.2, `BlockBootstrapConfidence` resamples contiguous blocks instead.

- **Deterministic input generation.** DPRNG generates reproducible inputs across runs. CPRNG, backed by [crypto/rand](https://pkg.go.dev/crypto/rand), is there when unpredictability or cryptographic quality is wanted instead.

## When to use rtcompare instead of `testing.B`

Use rtcompare when you want:

- Distribution-aware comparisons rather than single-number reports.
- Statistical confidence estimates for relative speedups (e.g., "A is at least 20% faster than B with 95% confidence").
- A library you can easily call from small programs, CI jobs, or dedicated comparison tools without the `testing` harness.

The standard `testing` package is excellent for microbenchmarks and tight per-op measurements. rtcompare complements it by focusing on sampling strategy, resampling inference, and reproducible comparisons across implementations.

## API highlights

Measuring:

- `Candidate` / `Batch` — one implementation under test, with optional `Setup` and `Teardown` that run outside the measured region.
- `Collect(a, b, CollectOptions)` — runs both candidates and returns one timing sample per repeat each, ready to hand to `CompareSamples`. Owns measurement order, warm-up, GC placement and batch sizing.
- `CalibrateInnerLoops(candidate, CalibrationOptions)` — sizes batches for a target quantization error. Called automatically when `CollectOptions.InnerLoops` is left at zero.

Judging:

- `CompareSamples(a, b, relativeGains, resamples)` — confidence per requested relative speedup. `CompareSamplesDefault` uses `DefaultResamples`.
- `BootstrapConfidence` — the same, returning a map, with control over the PRNG seed.
- `BlockBootstrapConfidence` — resamples contiguous blocks, for measurements correlated with their neighbours.
- `F2T(timesFaster)` — converts a multiplicative speedup to the relative threshold the API uses. It signals invalid input by returning NaN, which `CompareSamples` rejects with an error rather than silently answering.

Checking the measurement itself:

- `ValidateHarness(candidate, ValidationOptions)` — runs a candidate against itself and reports the noise floor, the tie rate, the drift rate and the autocorrelation. `Resolves(difference)` answers whether a result clears that floor.
- `DetectDrift(samples)` — tests a sample series for a trend across the run.

Primitives:

- `DPRNG` / `CPRNG` — deterministic and cryptographic generators with `Uint64`, `Float64` and `Uint32N`.
- `SampleTime()` / `DiffTimeStamps()` — high-resolution timestamps, and `GetSampleTimePrecision()` for the smallest interval they can resolve here.
- `Median` / `QuickMedian` / `Statistics` — small statistics helpers.

A note on the threshold of `0.0`: every threshold is evaluated as `delta >= t`, so at zero the question is "at least as fast", not "faster". Quantized timings tie often, and every tie counts towards it. Ask for a threshold above zero if you mean strictly faster.

Note on negative `relativeGains`: Negative thresholds are allowed and are
interpreted as tolerated relative slowdowns rather than speedups. A threshold
of `-0.05` means "A is within 5% of B" (i.e., A is not more than 5% slower
than B). Use negative values when you want to ask whether one implementation
is approximately as fast as another within a relative tolerance instead of
requiring a strict speedup.

### Choosing `resamples`

The number of bootstrap resamples controls the Monte‑Carlo error of the confidence estimates. Common recommendations from the bootstrap literature (Efron & Tibshirani; Davison & Hinkley) are:

- Use at least 1,000 resamples for reasonable standard-error estimation.
- Use 5,000–10,000 when you need stability in the tails, which here means confidences close to 0 or 1.

Note that these recommendations come from a literature concerned with confidence intervals, which this package does not compute; they carry over because the quantity it does compute, a proportion of replicates, has the same Monte-Carlo behaviour.

The Monte‑Carlo standard error of a proportion estimated from resamples decreases approximately as 1/sqrt(R) where R is the number of resamples. Increase `resamples` when you require low Monte‑Carlo noise (for example, precise reporting of extreme thresholds). See Efron & Tibshirani (1993) and Davison & Hinkley (1997) for more details.

See the package docs and the example in `cmd/rtcompare-example` for detailed usage.

## Contributing

Contributions welcome. Please open issues or PRs for bug reports, performance tweaks, or additional comparison strategies. Add tests for any behavioral changes and keep CPU/memory overheads small.

## License

MIT — see LICENSE file.
