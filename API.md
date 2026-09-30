# API guide

The README shows the one call most people need. This page lists everything the library exports and when to reach for it. The full reference, with every parameter, is on [pkg.go.dev](https://pkg.go.dev/github.com/TomTonic/rtcompare); [HOWTO.md](HOWTO.md) explains what to do with the results.

## Overview

### The one call


- `Compare(a, b, CompareOptions)` — runs the whole protocol and returns a `Report`. `Report.Resolved` is the short answer, `Report.Warnings` is the fine print, and the rest of the struct is the evidence: the samples, the estimate, the per-candidate validations, the drift tests and the resampling choice.
- `CompareContext(ctx, a, b, CompareOptions)` — the same, stopping between batches once `ctx` is done. `CompareOptions.Progress` reports the stages and each validation run, and `CompareOptions.MaxDuration` stops the validation, the dominant cost, from starting more runs once half the budget is gone.

### The individual steps, for when the summary is not enough

#### Measuring


- `Candidate` / `Batch` — one implementation under test, with optional `Setup` and `Teardown` that run outside the measured region.
- `Collect(a, b, CollectOptions)` — runs both candidates and returns one timing sample per repeat each, ready to hand to `CompareSamples`. Owns measurement order, warm-up, GC placement and batch sizing. The warm-up alternates the candidates for at least `WarmupDuration` (300 ms by default), so that neither starts the measurement with the caches to itself.
- `CalibrateInnerLoops(candidate, CalibrationOptions)` — sizes batches for a target quantization error. Called automatically when `CollectOptions.InnerLoops` is left at zero.

#### Judging


- `CompareSamples(a, b, relativeGains, resamples)` — confidence per requested relative speedup, as `Confidences`, a list sorted by threshold whose `At(threshold)` looks one up. Zero resamples select `DefaultResamples`.
- `EstimateDifference(a, b, EstimateOptions)` — the point estimate of the relative difference with a bootstrap interval around it, and with a `Seed` reproducibly. `Excludes(0)` asks whether a difference has been established at all, and `Ratio()` gives the factor B/A.
- `ConfidencesFor(a, b, thresholds, EstimateOptions)` — the confidences drawn from exactly the replicates `EstimateDifference` reads its interval from; with `Paired` for measurements taken in pairs, as `Report`'s are.
- `BootstrapConfidence` — the confidences of `CompareSamples`, for unpaired measurements, with control over the PRNG seed.
- `BlockBootstrapConfidence` — resamples contiguous blocks, for measurements correlated with their neighbours.
- `F2T(timesFaster)` — converts a multiplicative speedup to the relative threshold the API uses. It signals invalid input by returning NaN, which `CompareSamples` rejects with an error rather than silently answering.

#### Checking the measurement itself


- `ValidateHarness(candidate, ValidationOptions)` — runs a candidate against itself and reports the noise floor, the tie rate, the drift rate and the autocorrelation. `Resolves(difference)` answers whether a result clears that floor. The floor is the 90th percentile of the differences observed on identical code, not their maximum, so that it converges as you validate longer instead of growing; roughly one A/A run in ten exceeds it. Validate both candidates and use the worse floor.
- `ValidatePair(a, b, ValidationOptions)` — validates two candidates together, interleaved batch by batch, and returns one `HarnessValidation` each. Use it instead of two `ValidateHarness` calls before comparing the two: a candidate validated on its own last would start the comparison with a warm cache.
- `DetectDrift(samples)` — tests a sample series for a trend across the run.
- `Report.Suspended` — how long the machine slept during a `Compare`, from the gap between the wall clock and the monotonic clock.
- `Report.LiveHeap` — the program's live data; above 16 MB, `Compare` warns that one process is not enough and points to `multiproc`.

### Across processes


- `multiproc.Main(Options, pairs...)` / `multiproc.RunTest(t, Options, pairs...)` — the whole job in one call: each `Pair` says how to build candidate A and B, and the program is re-executed as child processes, each with its own heap layout and with the build order alternating, as many times as the first 6 processes show every pooled interval needs to be within ±2 points or ±10% of the difference (at most 40 processes serially). `Options.Parallel` runs the children in waves instead, see below. `multiproc.MainSuite(Options, suite)` and `multiproc.RunTestSuite(t, Options, suite)` take a suite instead, such as `workload.Suite`, and `multiproc.Suites(...)` combines several; `multiproc.Run(Options, suite)` is the general form underneath.
- `Combine(reports, level)` — pools per-process reports of one comparison into a `Pooled` result: a t interval over the per-process deltas, plus how far the processes scatter beyond their own intervals (`Inflation`, Cochran's Q, I²).
- `PerturbHeap(seed)` — allocates seeded filler in every small size class and one large block, so that data built afterwards lands at different addresses in each process.

### Primitives


- `DPRNG` / `CPRNG` — deterministic and cryptographic generators with `Uint64`, `Float64`, `Uint32N`, `Shuffle` and the sized integer and `Float32` methods. `NewDPRNG(seed)` gives every non-zero seed its own fixed sequence; seed `0` asks for a random one. `Shuffle` permutes, e.g. the order in which fixtures are built.
- `GetSampleTimePrecision()` — the smallest interval the clock resolves here.
- `Median` — the median, the mean of the two middle values for an even count.

### Workloads for mutable data structures

Package `github.com/TomTonic/rtcompare/workload`.


- `workload.Compare(target, a, b, Options)` — the whole job in one call: each `Structure` says how to create an empty structure and apply operations to it, and you get two reports, one for the steady state (per insertion or deletion, after one untimed cycle) and one for building from empty (per whole build, growth included).
- `workload.Cycle(target, Config)` / `workload.Build(target, Config)` — the streams: a cycle of insertions and deletions that ends where it started, and a build from empty with a realistic history.
- `workload.Replay` — replays a cycle on one structure instance, with the untimed first pass, and `Settle` to return it to its start state. `workload.Cursor` is the bare position, for doing it by hand.
- `workload.Check(ops, start, end)` — replays a stream against a model and reports the first invalid operation.
- `workload.Suite(name, target, a, b, Options)` — the same comparison for `multiproc.MainSuite` / `multiproc.RunTestSuite`, run in several processes with both answers pooled; the one to use once the structures outgrow the caches.

## Serial or parallel processes

`multiproc` runs its children one after another by default. `Options.Parallel: N` runs N at a time, which is usually the only way to a precise answer in reasonable time for data out of cache, but it measures a loaded machine and its results must not be pooled with a serial run's. [HOWTO.md](HOWTO.md#serial-or-parallel) explains when to use which.

## Thresholds

The threshold `0.0`: every threshold is evaluated as `delta >= t`, so at zero the question is "at least as fast", not "faster". Quantized timings tie often, and every tie counts towards it. Ask for a threshold above zero if you mean strictly faster.

Negative thresholds are allowed and are
interpreted as tolerated relative slowdowns rather than speedups. A threshold
of `-0.05` means "A is within 5% of B" (i.e., A is not more than 5% slower
than B). Use negative values when you want to ask whether one implementation
is approximately as fast as another within a relative tolerance instead of
requiring a strict speedup.

## Choosing `resamples`

The number of bootstrap resamples controls the Monte‑Carlo error of the confidence estimates. Common recommendations from the bootstrap literature (Efron & Tibshirani; Davison & Hinkley) are:

- Use at least 1,000 resamples for reasonable standard-error estimation.
- Use 5,000–10,000 when you need stability in the tails, which here means confidences close to 0 or 1.

Both quantities this package computes have that Monte-Carlo behaviour: the proportion of replicates meeting a threshold, and the quantiles of the resampled differences that `EstimateDifference` uses for its interval.

The Monte‑Carlo standard error of a proportion estimated from resamples decreases approximately as 1/sqrt(R) where R is the number of resamples. Increase `resamples` when you require low Monte‑Carlo noise (for example, precise reporting of extreme thresholds). See Efron & Tibshirani (1993) and Davison & Hinkley (1997) for more details.

See the package docs and the example in `cmd/rtcompare-example` for detailed usage.
