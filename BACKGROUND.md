# Technical background

Why `rtcompare` measures and computes what it does, with the numbers that justified each choice. None of this is needed to use the library; [HOWTO.md](HOWTO.md) is the place for that. This page is for readers who want to check the reasoning.



- **Batching is what beats the clock.** A single batch measurement is off by at most one clock tick `p`. Spread over `n` operations that is `p/n` per operation, so the relative error is `p/(n·c)` where `c` is one operation's cost. Since `n·c` is just the batch duration `T`, the whole thing collapses to `p/T`: the error depends only on how long a batch runs, not on how fast the operation is. `CalibrateInnerLoops` therefore searches for the smallest batch that reaches a target duration, which is why an expensive operation can calibrate to a batch of two while a cheap one needs thirteen thousand.

- **Pairs, not two separate bags.** `Collect` measures A and B in pairs, next to each other. `Compare` takes the difference from the ratio of each pair, 1 − median(Aᵢ/Bᵢ), and resamples whole pairs, so that a disturbance that hits both members of a pair, such as the clock speed changing, cancels instead of widening the interval. On memory-bound candidates that made the intervals 25–38% narrower; with disturbances common to both members, simulated, the unpaired interval covered at 99–100% for a nominal 95% at up to five times the width, while the paired one held 94–95%.

- **Bootstrap-based inference.** Rather than a single mean, rtcompare resamples the collected measurements. `CompareSamples` answers "how confident can I be that A beats B by at least x", which is what you want when a threshold is given. `EstimateDifference` answers "how large is the difference and how precisely is that known", which is what you want when none is. Its interval is a percentile bootstrap, measured to cover at 96–97% against a nominal 95%: conservative rather than optimistic, and well centred.

- **Why the median.** Each replicate is summarised by its median. Interference is one-sided, which argues for a low quantile instead, but simulation against a known difference says otherwise: below roughly 30% disturbed batches the median wins on RMSE, because with contamination on fewer than half the samples the middle one is already drawn from the clean part. Past 40% the median degrades sharply — and so does the noise floor `ValidateHarness` reports, from 1.2% to 20.5%, so that regime announces itself.

- **What resampling cannot see.** The bootstrap treats the samples as an unordered bag, which discards the order they were measured in. A machine that drifted during the run leaves no trace in its output. `DetectDrift` tests for that separately, using Spearman's rank correlation against measurement position; its false positive rate was verified at 4.80% against a nominal 5% over 6000 permutations of real measurement series.

- **Dependence between neighbouring measurements.** Resampling single observations also assumes they are exchangeable, and real measurements are mildly correlated. In AR(1) simulations the rate of false signals from identical inputs stayed at its nominal 10% up to a lag-1 correlation of 0.08, reached 13.5% at 0.2 and 21.7% at 0.4. `ValidateHarness` reports the correlation it observed; above roughly 0.2, `BlockBootstrapConfidence` resamples contiguous blocks instead.

- **Deterministic input generation.** `DPRNG` generates reproducible inputs across runs. `CPRNG`, backed by [crypto/rand](https://pkg.go.dev/crypto/rand), is there when unpredictability or cryptographic quality is wanted instead.

## What a result cannot tell you

Three limits are worth knowing before the first run, because neither is visible in a confidence figure.

**Attenuation.** What is measured is the loop, not the function. Whatever fixed per-operation cost the batch body carries — the loop itself, an accumulator, regenerating an input the candidate mutates — is present in both candidates and shrinks the difference between them. In a controlled experiment where the true difference was exactly 50%, the measured difference was 35%, because 1.81 ns/op of loop overhead sat on top of 2.13 ns/op of real work. Subtracting an empty-loop baseline does not repair it: the compiler optimizes an empty loop differently, and that correction recovered 2 of the 15 missing percentage points. Read a result as the speedup of the measured region, not of the isolated function.

**The process.** Every interval a single run reports covers the noise within that one process. A process fixes its memory layout for its whole life, and for data larger than the caches or full of pointers that layout alone has moved a difference by several points — 4 to 10 times the reported interval, and sometimes past zero. Where that applies, one process is one observation: run several with the `multiproc` package and read the pooled result. See [One process is one observation](HOWTO.md#one-process-is-one-observation).

**The noise floor.** Resampling quantifies how much an estimate would move if the same measurements were drawn again. It cannot see a bias that affected all of them equally, and will report a tight confidence around one. Measured on identical code, this package has seen apparent differences from a few tenths of a percent to well over one, carried with high confidence. `ValidateHarness` exists to measure that floor for your machine and your options; a result below it has resolved nothing, however confident the number looks.
