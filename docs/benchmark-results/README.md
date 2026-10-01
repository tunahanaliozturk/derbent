# Benchmark results

These files hold what Derbent's gate costs per call, measured on GitHub's hosted Linux and Windows runners on 2026-10-01 (19:43 UTC), on the commit the v1.0.0 release is cut from.

- Run: https://github.com/tunahanaliozturk/derbent/actions/runs/36916385907 (the `bench` workflow, `.github/workflows/bench.yml`, on the branch `main`)
- Commit measured: `54238a0f15714f5c4de2c2e0b4383d13d13a4a35`
- Linux: GitHub-hosted `ubuntu-latest`, which the run log names as image `ubuntu-24.04`, version `20260927.320.1`, on an AMD EPYC 9V45 with 4 vCPUs
- Windows: GitHub-hosted `windows-latest`, which the run log names as image `windows-2025-vs2026`, version `20260925.250.1`, on an AMD EPYC 9V45 with 4 vCPUs

| File | What it is |
|------|------------|
| `linux-raw.txt`, `windows-raw.txt` | The `go test -bench` output, ten runs of each benchmark. The first line names the runner image. |
| `linux.txt`, `windows.txt` | The same results summarised by benchstat: the median of the ten runs and its 95% confidence interval, with the gate compared against its baseline. |

## What is measured

The benchmarks live in `cmd/derbent/bench_test.go`. Every process they talk to is real and has started and answered once before the timer starts.

- `BenchmarkEcho` makes one MCP tool call to a small echo server. `path=direct` calls the server itself. `path=gate` calls it through a `derbent mcp` process with one allow rule, so each call takes the extra stdio hop through the gate, a rule decision and a receipt appended to SQLite before the answer comes back. After the run the benchmark checks that the database holds one receipt per call.
- `BenchmarkHook` runs one pre-tool hook call the way a CLI does: `call=hook` starts `derbent gate --agent claude` with a Claude `PreToolUse` input on stdin, which loads the config, opens the database, decides the call with an allow rule and writes a receipt. `call=spawn` starts the same binary as `derbent version`, which exits at once. That is the part of the cost any `derbent` invocation pays: starting this test binary, including its package initialisation.

Calls are made one after another on one session, so calls per second is the inverse of the mean latency, not throughput under concurrent load. The binary the benchmarks start, in both hook rows and as the gate, is the Go test binary of `cmd/derbent`, not a release build.

All the numbers come from one workflow run on shared GitHub-hosted runners. On each OS, all ten runs of each benchmark ran in one job on one VM, so the confidence intervals only cover noise within that VM. Another run, or another runner, may differ by more than the intervals shown, and the run the day before on the same code did (see Reading the numbers). Both jobs ran on an `AMD EPYC 9V45 96-Core Processor`, as the raw files name it, but on different VMs under different operating systems. Compare the gate with its baseline within each OS, not one OS with the other.

The p99 of one run is a single sample: index `n*99/100` of that run's sorted latencies. An echo run has 2000 samples, so its p99 is about the 20th largest. A hook run has only 200, so its p99 is about the second largest. Judge the hook by its p50, which stays within ±7% on both OSes in this run. The p99 and calls per second of the hook rows are noisy:

- On Linux the hook's p99 was between 56.35 ms and 248.11 ms across the ten runs, and above 170 ms in seven of them, while its p50 stayed between 6.58 ms and 8.19 ms. Its mean, which calls per second inverts, was between 10.26 ms and 27.20 ms, so in most runs a share of calls took far longer than the p50. The spawn rows have no such tail (p99 4.030 ms), so it comes from the hook's own work rather than the process start. The run before (EPYC 7763) put the Linux hook's p99 at 10.678 ms. What the slow calls wait on has not been profiled.
- On Linux the gate row of the echo benchmark shows the same shape in four of its ten runs: a mean between 1.215 ms and 1.730 ms, against 0.620 ms to 0.781 ms in the other six, while the p50 stayed between 455 µs and 488 µs and the p99 was at most 1.922 ms in every run. That points at a few slow calls above the p99, fewer than 20 of a run's 2000, and gives this row's calls per second its ±48%.
- On Windows the hook's p99 was 1.85 s and 3.20 s in two runs and between 62.37 ms and 167.03 ms in the other eight, so its median has an interval of ±1819% and its calls per second ±52%.

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
| p50 | 144.0 µs ± 3% | 470.0 µs ± 2% |
| p99 | 413.5 µs ± 8% | 1317.0 µs ± 20% |
| Calls per second | 5.851k ± 3% | 1.336k ± 48% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 3.263 ms ± 4% | 6.766 ms ± 7% |
| p99 | 4.030 ms ± 12% | 204.259 ms ± 62% |
| Calls per second | 299.25 ± 5% | 51.23 ± 54% |

## Windows

From `windows.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 146.5 µs ± 4% | 474.0 µs ± 3% |
| p99 | 863.0 µs ± 40% | 2983.5 µs ± 9% |
| Calls per second | 5.010k ± 2% | 1.011k ± 10% |

| Hook call | Spawn baseline | `derbent gate`, allowed |
|-----------|----------------|-------------------------|
| p50 | 25.07 ms ± 7% | 44.29 ms ± 5% |
| p99 | 35.28 ms ± 30% | 96.54 ms ± 1819% |
| Calls per second | 39.07 ± 8% | 20.88 ± 52% |

## Reading the numbers

The differences below are between the medians in the tables above.

- An MCP call through the gate takes 326.0 µs longer at p50 than a direct call on Linux, and 327.5 µs longer on Windows.
- A hook call takes 3.503 ms longer at p50 than starting the binary and exiting on Linux, and 19.22 ms longer on Windows. On Windows most of a hook call's cost is the process start itself: 25.07 ms of its 44.29 ms.
- The run published before this one (run 36359801469, commit `0d5daa9`, after milestone 6) measured those differences at 521.0 µs and 680.0 µs for an MCP call, and at 3.065 ms and 36.12 ms for a hook call. That run was on an AMD EPYC 7763 on both OSes, so part of the change is a different machine, not the code. Between the two commits milestone 7 landed, and commit `38e0ce7` dropped the read-only open of the database that milestone 6 had added to every hook call. With the CPU and the code both changing, these two runs cannot say how much of the change each one explains.
- The day before, run 36783411007 measured commit `4c5d323`, which differs from this one only in `docs/backlog.md` and `docs/design.md`, on an AMD EPYC 9V74. It put those differences at 517.5 µs on Linux and 459.5 µs on Windows for an MCP call, and at 2.535 ms on Linux and 33.51 ms on Windows for a hook call (64.05 ms against 30.54 ms for the spawn). The same code, on another runner, moved by more than this run's intervals, so read every number here as one runner's, not as a bound.
