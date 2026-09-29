# rtcompare

[![Go Reference](https://pkg.go.dev/badge/github.com/TomTonic/rtcompare.svg)](https://pkg.go.dev/github.com/TomTonic/rtcompare)
[![Linter](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml/badge.svg)](https://github.com/TomTonic/rtcompare/actions/workflows/linter.yml)
[![Tests](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml/badge.svg?branch=main)](https://github.com/TomTonic/rtcompare/actions/workflows/coverage.yml)
![coverage](https://raw.githubusercontent.com/TomTonic/rtcompare/badges/.badges/main/coverage.svg)
[![Vulnerabilities](https://img.shields.io/endpoint?url=https://gist.githubusercontent.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db/raw/release_scan.json)](https://gist.github.com/TomTonic/cbfad7cf38e139ba898fe41386efa4db#file-release_scan-md)

## Statistically significant runtime comparison for codepaths in golang

rtcompare is a small Go library for deciding whether one code path is genuinely faster than another. It measures both candidates, resamples the measurements to estimate how confident that conclusion is, and — the part that distinguishes it — measures what the machine invents on its own so that the conclusion can be read against it.

New to this and not a statistics person? **[Read HOWTO.md](HOWTO.md)** — it walks through what to actually do, in plain language, including what each warning means and what to do about it.

Keywords: benchmarking, performance, bootstrap, runtime comparison, statistics, deterministic prng, go

## Features

- Answer the whole question in one call: `Compare` sizes the batches, measures what the harness invents on its own, runs the comparison, tests for drift, picks the resampling scheme from the dependence it observed, and reports a verdict with the fine print that qualifies it.
- Collect timing or memory samples for two implementations under a harness that interleaves their measurement order, keeps setup out of the measured region, and places garbage collection deterministically.
- Size batches automatically, so that the system clock contributes at most a chosen share of error. This is what makes differences far below the clock's resolution measurable: a per-operation difference of 1.89 ns was recovered to within 0.07 percentage points against a 41 ns clock floor.
- Estimate, by bootstrap resampling, the confidence that A beats B by at least a given relative margin.
- Measure the harness against itself, so that a result can be compared with the difference the same setup reports between two runs of identical code.
- Detect a trend across a measurement run, which resampling structurally cannot see because it discards the order the samples arrived in.
- Resample in blocks when the measurements are correlated enough that treating them as independent would overstate confidence.
- Run a comparison in several processes, each with its own heap layout, and pool the results into an interval that covers the scatter between processes (`multiproc`, `Combine`, `PerturbHeap`).
- Deterministic PRNG for reproducible inputs, and a crypto/rand-backed one where unpredictability is wanted.
- Compare mutable data structures under realistic insert/delete workloads in one call (`workload`), with separate answers for the steady state and for building to size; the streams are valid by construction, reproducible from a seed, and cyclic, so that a batch can replay them endlessly without rebuilding the structure.

## What this cannot tell you

Two limits are worth knowing before the first run, because neither is visible in a confidence figure.

**Attenuation.** What is measured is the loop, not the function. Whatever fixed per-operation cost the batch body carries — the loop itself, an accumulator, regenerating an input the candidate mutates — is present in both candidates and shrinks the difference between them. In a controlled experiment where the true difference was exactly 50%, the measured difference was 35%, because 1.81 ns/op of loop overhead sat on top of 2.13 ns/op of real work. Subtracting an empty-loop baseline does not repair it: the compiler optimizes an empty loop differently, and that correction recovered 2 of the 15 missing percentage points. Read a result as the speedup of the measured region, not of the isolated function.

**The process.** Every interval a single run reports covers the noise within that one process. A process fixes its memory layout for its whole life, and for data larger than the caches or full of pointers that layout alone has moved a difference by several points — 4 to 10 times the reported interval, and sometimes past zero. Where that applies, one process is one observation: run several with the `multiproc` package and read the pooled result. See [One process is one observation](HOWTO.md#one-process-is-one-observation).

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

	// Everything is left at its default: the batches are sized so that the clock
	// contributes at most a tenth of a percent, both candidates are validated
	// against themselves, the order is interleaved, and the resampling scheme is
	// chosen from the dependence actually measured.
	report, err := rtcompare.Compare(candidateA, candidateB, rtcompare.CompareOptions{
		Thresholds: []float64{0.05, 0.10, 0.20},
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(report)

	if report.Resolved {
		fmt.Printf("A is faster by %s\n", report.Estimate)
	}
}
```

which prints something like

```
A 712.7 per op, B 1262 per op
difference +43.52% [+42.31%, +44.48%] at 95% confidence
noise floor 1.765%, autocorrelation +0.344, resampled in blocks of 5
resolved: A is faster than B
  warning: candidate B drifted during the run, shifting -7.12% from its first
  half to its second; the machine did not hold still
  confidence that A beats B by 5.00%: 100.0%
```

`Compare` validates both candidates against themselves before comparing them, so it costs a few seconds. Set `SkipValidation` to pay only for the measurement, accepting that the result then has no noise floor to be read against. The individual steps are all exported too, and `cmd/rtcompare-example` shows both: the one call, and then the same measurements taken apart by hand.

Not sure what a warning like "resampled in blocks" or "does not clear the noise floor" means, or what to do about it? **[HOWTO.md](HOWTO.md)** goes through each step `Compare` performs and each warning it can produce, with a plain-language explanation and a concrete fix.

## Technical background

- **Batching is what beats the clock.** A single batch measurement is off by at most one clock tick `p`. Spread over `n` operations that is `p/n` per operation, so the relative error is `p/(n·c)` where `c` is one operation's cost. Since `n·c` is just the batch duration `T`, the whole thing collapses to `p/T`: the error depends only on how long a batch runs, not on how fast the operation is. `CalibrateInnerLoops` therefore searches for the smallest batch that reaches a target duration, which is why an expensive operation can calibrate to a batch of two while a cheap one needs thirteen thousand.

- **Bootstrap-based inference.** Rather than a single mean, rtcompare resamples the collected measurements. `CompareSamples` answers "how confident can I be that A beats B by at least x", which is what you want when a threshold is given. `EstimateDifference` answers "how large is the difference and how precisely is that known", which is what you want when none is. Its interval is a percentile bootstrap, measured to cover at 96–97% against a nominal 95%: conservative rather than optimistic, and well centred.

- **Why the median.** Each replicate is summarised by its median. Interference is one-sided, which argues for a low quantile instead, but simulation against a known difference says otherwise: below roughly 30% disturbed batches the median wins on RMSE, because with contamination on fewer than half the samples the middle one is already drawn from the clean part. Past 40% the median degrades sharply — and so does the noise floor `ValidateHarness` reports, from 1.2% to 20.5%, so that regime announces itself.

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

The one call:

- `Compare(a, b, CompareOptions)` — runs the whole protocol and returns a `Report`. `Report.Resolved` is the short answer, `Report.Warnings` is the fine print, and the rest of the struct is the evidence: the samples, the estimate, the per-candidate validations, the drift tests and the resampling choice.

The individual steps, for when the summary is not enough:

Measuring:

- `Candidate` / `Batch` — one implementation under test, with optional `Setup` and `Teardown` that run outside the measured region.
- `Collect(a, b, CollectOptions)` — runs both candidates and returns one timing sample per repeat each, ready to hand to `CompareSamples`. Owns measurement order, warm-up, GC placement and batch sizing. The warm-up alternates the candidates for at least `WarmupDuration` (300 ms by default), so that neither starts the measurement with the caches to itself.
- `CalibrateInnerLoops(candidate, CalibrationOptions)` — sizes batches for a target quantization error. Called automatically when `CollectOptions.InnerLoops` is left at zero.

Judging:

- `CompareSamples(a, b, relativeGains, resamples)` — confidence per requested relative speedup. `CompareSamplesDefault` uses `DefaultResamples`.
- `EstimateDifference(a, b, level, resamples)` — the point estimate of the relative difference with a bootstrap interval around it. `Excludes(0)` asks whether a difference has been established at all.
- `BootstrapConfidence` — the same, returning a map, with control over the PRNG seed.
- `BlockBootstrapConfidence` — resamples contiguous blocks, for measurements correlated with their neighbours.
- `F2T(timesFaster)` — converts a multiplicative speedup to the relative threshold the API uses. It signals invalid input by returning NaN, which `CompareSamples` rejects with an error rather than silently answering.

Checking the measurement itself:

- `ValidateHarness(candidate, ValidationOptions)` — runs a candidate against itself and reports the noise floor, the tie rate, the drift rate and the autocorrelation. `Resolves(difference)` answers whether a result clears that floor. The floor is the 90th percentile of the differences observed on identical code, not their maximum, so that it converges as you validate longer instead of growing; roughly one A/A run in ten exceeds it. Validate both candidates and use the worse floor.
- `ValidatePair(a, b, ValidationOptions)` — validates two candidates together, interleaved batch by batch, and returns one `HarnessValidation` each. Use it instead of two `ValidateHarness` calls before comparing the two: a candidate validated on its own last would start the comparison with a warm cache.
- `DetectDrift(samples)` — tests a sample series for a trend across the run.
- `Report.Suspended` — how long the machine slept during a `Compare`, from the gap between the wall clock and the monotonic clock.
- `Report.LiveHeap` — the program's live data; above 16 MB, `Compare` warns that one process is not enough and points to `multiproc`.

Across processes:

- `multiproc.Main(Options, pairs...)` / `multiproc.RunTest(t, Options, pairs...)` — the whole job in one call: each `Pair` says how to build candidate A and B, and the program is re-executed as child processes, each with its own heap layout and with the build order alternating, until every pooled interval is within ±2 points or ±10% of the difference (at least 5, at most 40 processes serially). `Options.Parallel` runs the children in waves instead, see below. `multiproc.Run(Options, suite)` does the same for a suite function of your own.
- `Combine(reports, level)` — pools per-process reports of one comparison into a `Pooled` result: a t interval over the per-process deltas, plus how far the processes scatter beyond their own intervals (`Inflation`, Cochran's Q, I²).
- `PerturbHeap(seed)` — allocates seeded filler in every small size class and one large block, so that data built afterwards lands at different addresses in each process.

Primitives:

- `DPRNG` / `CPRNG` — deterministic and cryptographic generators with `Uint64`, `Float64` and `Uint32N`. `DPRNG.Shuffle` permutes, e.g. the order in which fixtures are built.
- `SampleTime()` / `DiffTimeStamps()` — high-resolution timestamps, and `GetSampleTimePrecision()` for the smallest interval they can resolve here.
- `Median` / `QuickMedian` / `Statistics` — small statistics helpers.

Workloads for mutable data structures (`github.com/TomTonic/rtcompare/workload`):

- `workload.Compare(target, a, b, Options)` — the whole job in one call: each `Structure` says how to create an empty structure and apply operations to it, and you get two reports, one for the steady state (per insertion or deletion, after one untimed cycle) and one for building from empty (per whole build, growth included).
- `workload.Cycle(target, Config)` / `workload.Build(target, Config)` — the streams: a cycle of insertions and deletions that ends where it started, and a build from empty with a realistic history.
- `workload.Replay` — replays a cycle on one structure instance, with the untimed first pass, and `Settle` to return it to its start state. `workload.Cursor` is the bare position, for doing it by hand.
- `workload.Check(ops, start, end)` — replays a stream against a model and reports the first invalid operation.

### Serial or parallel processes

`multiproc` runs its children one after another by default, each with the machine to itself. `Options.Parallel: N` runs N at a time, in waves, and budgets 10 waves by default. Out of cache that is usually the only way to a precise answer in reasonable time: the variance of the pooled estimate is roughly σ²_between/P + σ²_within/(P·R) for P processes of R samples, the first term dominates by far, and only more processes reduce it. Running them simultaneously multiplies P per hour by N.

Parallel runs measure a loaded machine: the children share the last-level cache, memory bandwidth and clock headroom, like a program with neighbours in production. That is legitimate, but it is a different regime, so never pool or compare its results with a serial run's; `Results.Parallel` records which one ran. Keep N at or below the number of physical cores, since two children on SMT siblings share L1 and L2 and disturb each other far more than neighbours on separate cores. Each child gets `GOMAXPROCS` = CPUs/N (at least 2) unless you set it yourself.

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

Both quantities this package computes have that Monte-Carlo behaviour: the proportion of replicates meeting a threshold, and the quantiles of the resampled differences that `EstimateDifference` uses for its interval.

The Monte‑Carlo standard error of a proportion estimated from resamples decreases approximately as 1/sqrt(R) where R is the number of resamples. Increase `resamples` when you require low Monte‑Carlo noise (for example, precise reporting of extreme thresholds). See Efron & Tibshirani (1993) and Davison & Hinkley (1997) for more details.

See the package docs and the example in `cmd/rtcompare-example` for detailed usage.

## Contributing

Contributions welcome. Please open issues or PRs for bug reports, performance tweaks, or additional comparison strategies. Add tests for any behavioral changes and keep CPU/memory overheads small.

## License

MIT — see LICENSE file.
