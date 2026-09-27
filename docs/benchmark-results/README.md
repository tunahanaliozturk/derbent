# Benchmark results

These files hold what Derbent's gate costs per call, measured on GitHub's hosted Linux and Windows runners on 2026-09-27.

- Run: https://github.com/tunahanaliozturk/derbent/actions/runs/36312269745 (the `bench` workflow, `.github/workflows/bench.yml`)
- Commit measured: `529b2aaf703ac54d013188c2b7a3faf397ce0281`
- Linux: GitHub-hosted `ubuntu-latest`, which the run log names as image `ubuntu-24.04`, version `20260920.314.1`, on an AMD EPYC 9V74 with 4 vCPUs
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

All the numbers come from one workflow run on shared GitHub-hosted runners. On each OS, all ten runs of each benchmark ran in one job on one VM, so the confidence intervals only cover noise within that VM. Another run, or another runner, may differ by more than the intervals shown. The Linux job ran on an `AMD EPYC 9V74 80-Core Processor` and the Windows job on an `AMD EPYC 7763 64-Core Processor`, as the raw files name them. Compare the gate with its baseline within each OS, not one OS with the other.

The p99 of one run is a single sample: index `n*99/100` of that run's sorted latencies. An echo run has 2000 samples, so its p99 is about the 20th largest. A hook run has only 200, so its p99 is about the second largest. That is why the Windows hook p99 varies by about ±17% across runs while its p50 stays within ±1%.

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
| p50 | 264.0 µs ± 3% | 731.5 µs ± 1% |
| p99 | 664.0 µs ± 2% | 1438.0 µs ± 3% |
| Calls per second | 3.205k ± 2% | 1.248k ± 0% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 4.282 ms ± 0% | 6.374 ms ± 1% |
| p99 | 4.673 ms ± 4% | 6.907 ms ± 7% |
| Calls per second | 233.1 ± 1% | 156.5 ± 1% |

## Windows

From `windows.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 348.5 µs ± 5% | 968.0 µs ± 1% |
| p99 | 949.0 µs ± 2% | 2003.0 µs ± 7% |
| Calls per second | 2378.5 ± 3% | 772.2 ± 6% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 46.87 ms ± 1% | 68.96 ms ± 1% |
| p99 | 56.90 ms ± 5% | 96.33 ms ± 17% |
| Calls per second | 20.94 ± 1% | 14.23 ± 2% |

## Reading the numbers

The differences below are between the medians in the tables above.

- An MCP call through the gate takes 467.5 µs longer at p50 than a direct call on Linux, and 619.5 µs longer on Windows.
- A hook call takes 2.092 ms longer at p50 than starting the binary and exiting on Linux, and 22.09 ms longer on Windows. On Windows most of a hook call's cost is the process start itself: 46.87 ms of its 68.96 ms.
