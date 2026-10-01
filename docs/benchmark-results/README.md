# Benchmark results

These files hold what Derbent's gate costs per call, measured on GitHub's hosted Linux and Windows runners on 2026-10-01 (20:13 UTC), on the code the v1.0.0 release is cut from.

- Run: https://github.com/tunahanaliozturk/derbent/actions/runs/36919933154 (the `bench` workflow, `.github/workflows/bench.yml`, on the branch `main`)
- Commit measured: `592d7c82b8d0f218feffde8f5d63c218d11a30d2`
- Linux: GitHub-hosted `ubuntu-latest`, which the run log names as image `ubuntu-24.04`, version `20260927.320.1`, on an Intel Xeon Platinum 8370C with 4 vCPUs
- Windows: GitHub-hosted `windows-latest`, which the run log names as image `windows-2025-vs2026`, version `20260925.250.1`, on an AMD EPYC 9V74 with 4 vCPUs

| File | What it is |
|------|------------|
| `linux-raw.txt`, `windows-raw.txt` | The `go test -bench` output, ten runs of each benchmark. The first line names the runner image. |
| `linux.txt`, `windows.txt` | The same results summarised by benchstat: the median of the ten runs and its 95% confidence interval, with the gate compared against its baseline. |

## What is measured

The benchmarks live in `cmd/derbent/bench_test.go`. Every process they talk to is real and has started and answered once before the timer starts.

- `BenchmarkEcho` makes one MCP tool call to a small echo server. `path=direct` calls the server itself. `path=gate` calls it through a `derbent mcp` process with one allow rule, so each call takes the extra stdio hop through the gate, a rule decision and a receipt appended to SQLite before the answer comes back. After the run the benchmark checks that the database holds one receipt per call.
- `BenchmarkHook` runs one pre-tool hook call the way a CLI does: `call=hook` starts `derbent gate --agent claude` with a Claude `PreToolUse` input on stdin, which loads the config, opens the database, decides the call with an allow rule and writes a receipt. `call=spawn` starts the same binary as `derbent version`, which exits at once. That is the part of the cost any `derbent` invocation pays: starting this test binary, including its package initialisation.

Calls are made one after another on one session, so calls per second is the inverse of the mean latency, not throughput under concurrent load. The binary the benchmarks start, in both hook rows and as the gate, is the Go test binary of `cmd/derbent`, not a release build.

All the numbers come from one workflow run on shared GitHub-hosted runners. On each OS, all ten runs of each benchmark ran in one job on one VM, so the confidence intervals only cover noise within that VM. Another run, or another runner, may differ by more than the intervals shown, and runs of the same code have (see Reading the numbers). The Linux job ran on an `Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz` and the Windows job on an `AMD EPYC 9V74 80-Core Processor`, as the raw files name them. Compare the gate with its baseline within each OS, not one OS with the other.

The p99 of one run is a single sample: index `n*99/100` of that run's sorted latencies. An echo run has 2000 samples, so its p99 is about the 20th largest. A hook run has only 200, so its p99 is about the second largest. Judge the hook by its p50, which stays within ±4% on both OSes in this run. Two runs in this one stand out, and the medians hide both:

- On Linux one of the ten runs through the gate averaged 30.86 ms per call, against 0.80 ms to 0.84 ms in the other nine, while its p50 was 800 µs and its p99 1.627 ms. Its 2000 calls took about 62 seconds instead of under 2, so some of the at most 20 calls above its p99 waited about a minute between them.
- On Windows one hook run had a p99 of 769.05 ms and a mean of 96.21 ms; the other nine had a p99 between 88.15 ms and 125.71 ms and a mean between 64.41 ms and 71.78 ms.

The run just before this one, 36916385907 on an AMD EPYC 9V45 and the same code, showed a Linux hook p99 above 170 ms in seven of its ten runs, up to 248.11 ms, against a p50 near 6.8 ms. It did not repeat here: the Linux hook's p99 stayed between 7.53 ms and 10.72 ms. In both runs only rows that write to SQLite showed stalls of that size, and nothing has profiled them. Treat p99 and calls per second from shared runners with care.

On Windows the clock behind `time.Since` moves in steps of 0.5 to 15.6 ms, which is coarser than one MCP call, so the per-call latencies there are read from the performance counter (`cmd/derbent/stopwatch_windows_test.go`).

## Commands

The workflow ran these steps on each runner, in bash:

```bash
echo "runner-image: $ImageOS $ImageVersion" > raw.txt
go test ./cmd/derbent/ -run '^$' -bench '^BenchmarkEcho$' -benchtime 2000x -count 10 -timeout 30m >> raw.txt
go test ./cmd/derbent/ -run '^$' -bench '^BenchmarkHook$' -benchtime 200x -count 10 -timeout 30m >> raw.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260908200009-22c9c6c9d4da -filter .name:Echo -col /path raw.txt > summary.txt
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260908200009-22c9c6c9d4da -filter .name:Hook -col /call raw.txt >> summary.txt
```

To measure again, run `gh workflow run bench.yml` and download the artefacts with `gh run download`.

## Linux

From `linux.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 256.5 µs ± 4% | 756.0 µs ± 2% |
| p99 | 632.0 µs ± 6% | 1522.5 µs ± 7% |
| Calls per second | 3.307k ± 2% | 1.224k ± 3% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 4.292 ms ± 4% | 6.421 ms ± 1% |
| p99 | 5.357 ms ± 18% | 7.895 ms ± 29% |
| Calls per second | 230.4 ± 3% | 153.6 ± 2% |

## Windows

From `windows.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 293.0 µs ± 3% | 909.0 µs ± 1% |
| p99 | 825.5 µs ± 5% | 2152.0 µs ± 21% |
| Calls per second | 2774.0 ± 2% | 812.5 ± 7% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 40.40 ms ± 1% | 64.51 ms ± 2% |
| p99 | 47.69 ms ± 9% | 99.47 ms ± 26% |
| Calls per second | 24.34 ± 3% | 15.21 ± 8% |

## Reading the numbers

The differences below are between the medians in the tables above.

- An MCP call through the gate takes 499.5 µs longer at p50 than a direct call on Linux, and 616.0 µs longer on Windows.
- A hook call takes 2.129 ms longer at p50 than starting the binary and exiting on Linux, and 24.11 ms longer on Windows. On Windows most of a hook call's cost is the process start itself: 40.40 ms of its 64.51 ms.
- The run published before this one (run 36359801469, commit `0d5daa9`, after milestone 6) measured those differences at 521.0 µs and 680.0 µs for an MCP call, and at 3.065 ms and 36.12 ms for a hook call. Since then milestone 7 landed, and commit `38e0ce7` dropped the read-only open of the database that milestone 6 had added to every hook call. The runner CPUs also differ from run to run: an AMD EPYC 7763 on both OSes in that run, an EPYC 9V74 in run 36783411007, an EPYC 9V45 in run 36916385907, and an Intel Xeon Platinum 8370C and an EPYC 9V74 in this one. So the change from the old numbers mixes a code change with a different machine, and these runs cannot say how much each explains.
- Even the same code on the same CPU model moves by more than one run's intervals. Run 36783411007, on commit `4c5d323`, which differs from this one only in documentation, ran its Windows job on an EPYC 9V74 too and put the Windows differences at 459.5 µs for an MCP call and 33.51 ms for a hook call (its hook p50 interval was ±93%). Read every number here as one runner's, not as a bound.
