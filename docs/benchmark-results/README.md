# Benchmark results

These files hold what Derbent's gate costs per call, measured on GitHub's hosted Linux and Windows runners on 2026-09-27 (23:46 UTC), after milestone 6.

- Run: https://github.com/tunahanaliozturk/derbent/actions/runs/36359801469 (the `bench` workflow, `.github/workflows/bench.yml`, on the branch `m6/final-fixes`)
- Commit measured: `0d5daa9bd432bfc6b81157d8249c61ddbd287331`
- Linux: GitHub-hosted `ubuntu-latest`, which the run log names as image `ubuntu-24.04`, version `20260920.314.1`, on an AMD EPYC 7763 with 4 vCPUs
- Windows: GitHub-hosted `windows-latest`, which the run log names as image `windows-2025-vs2026`, version `20260922.246.2`, on an AMD EPYC 7763 with 4 vCPUs

| File | What it is |
|------|------------|
| `linux-raw.txt`, `windows-raw.txt` | The `go test -bench` output, ten runs of each benchmark. The first line names the runner image. |
| `linux.txt`, `windows.txt` | The same results summarised by benchstat: the median of the ten runs and its 95% confidence interval, with the gate compared against its baseline. |

## What is measured

The benchmarks live in `cmd/derbent/bench_test.go`. Every process they talk to is real and has started and answered once before the timer starts.

- `BenchmarkEcho` makes one MCP tool call to a small echo server. `path=direct` calls the server itself. `path=gate` calls it through a `derbent mcp` process with one allow rule, so each call takes the extra stdio hop through the gate, a rule decision and a receipt appended to SQLite before the answer comes back. After the run the benchmark checks that the database holds one receipt per call.
- `BenchmarkHook` runs one pre-tool hook call the way a CLI does: `call=hook` starts `derbent gate --agent claude` with a Claude `PreToolUse` input on stdin, which loads the config, opens the database, decides the call with an allow rule and writes a receipt. `call=spawn` starts the same binary as `derbent version`, which exits at once. That is the part of the cost any `derbent` invocation pays: starting this test binary, including its package initialisation.

Calls are made one after another on one session, so calls per second is the inverse of the mean latency, not throughput under concurrent load. The binary the benchmarks start, in both hook rows and as the gate, is the Go test binary of `cmd/derbent`, not a release build.

All the numbers come from one workflow run on shared GitHub-hosted runners. On each OS, all ten runs of each benchmark ran in one job on one VM, so the confidence intervals only cover noise within that VM. Another run, or another runner, may differ by more than the intervals shown. Both jobs ran on an `AMD EPYC 7763 64-Core Processor`, as the raw files name it, but on different VMs under different operating systems. Compare the gate with its baseline within each OS, not one OS with the other.

The p99 of one run is a single sample: index `n*99/100` of that run's sorted latencies. An echo run has 2000 samples, so its p99 is about the 20th largest. A hook run has only 200, so its p99 is about the second largest. That is why the Windows hook p99 varies so much across runs: in four of the ten it was between 0.38 s and 1.71 s, in the other six between 0.11 s and 0.14 s, so its median has an interval of ±528%, while the p50 stays within ±4%.

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
| p50 | 304.5 µs ± 1% | 825.5 µs ± 1% |
| p99 | 731.0 µs ± 4% | 1565.5 µs ± 3% |
| Calls per second | 2.848k ± 1% | 1.109k ± 2% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 4.316 ms ± 1% | 7.381 ms ± 2% |
| p99 | 4.815 ms ± 1% | 10.678 ms ± 6% |
| Calls per second | 230.9 ± 1% | 133.9 ± 1% |

## Windows

From `windows.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 353.0 µs ± 2% | 1033.0 µs ± 1% |
| p99 | 941.0 µs ± 6% | 2212.0 µs ± 5% |
| Calls per second | 2371.5 ± 2% | 643.8 ± 7% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 46.91 ms ± 1% | 83.03 ms ± 4% |
| p99 | 57.64 ms ± 6% | 135.42 ms ± 528% |
| Calls per second | 20.84 ± 1% | 11.79 ± 28% |

## Reading the numbers

The differences below are between the medians in the tables above.

- An MCP call through the gate takes 521.0 µs longer at p50 than a direct call on Linux, and 680.0 µs longer on Windows.
- A hook call takes 3.065 ms longer at p50 than starting the binary and exiting on Linux, and 36.12 ms longer on Windows. On Windows most of a hook call's cost is the process start itself: 46.91 ms of its 83.03 ms.
- The run before milestone 6 (run 36312269745, commit `529b2aa`) measured those differences at 467.5 µs and 619.5 µs for an MCP call, and at 2.092 ms and 22.09 ms for a hook call. Milestone 6 added to every hook call a read-only open of the database before the real one, a second walk up the directory tree for the checkout's root and a check of the project rules file, and to every MCP call that check too. The two runs used different Linux CPUs, so only the differences within each OS compare.
