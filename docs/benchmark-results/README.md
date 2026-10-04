# Benchmark results

These files hold what Derbent's gate costs per call, measured on GitHub's hosted Linux and Windows runners on 2026-10-02 (07:34 UTC), on the v1.0.0 tag plus one commit that adds a third hook benchmark.

- Run: https://github.com/tunahanaliozturk/derbent/actions/runs/36979294321 (the `bench` workflow, `.github/workflows/bench.yml`, on the branch `bench/hook-beside-gate`)
- Commit measured: `5470c5ed604291528e5b79b4f7d57fb5a24ffb96`
- Linux: GitHub-hosted `ubuntu-latest`, which the run log names as image `ubuntu-24.04`, version `20260927.320.1`, on an AMD EPYC 9V74 with 4 vCPUs
- Windows: GitHub-hosted `windows-latest`, which the run log names as image `windows-2025-vs2026`, version `20260925.250.1`, on an AMD EPYC 7763 with 4 vCPUs

| File | What it is |
|------|------------|
| `linux-raw.txt`, `windows-raw.txt` | The `go test -bench` output, ten runs of each benchmark. The first line names the runner image. |
| `linux.txt`, `windows.txt` | The same results summarised by benchstat: the median of the ten runs and its 95% confidence interval, with the gate compared against its baseline. |

## What is measured

The benchmarks live in `cmd/derbent/bench_test.go`. Every process they talk to is real and has started and answered once before the timer starts.

- `BenchmarkEcho` makes one MCP tool call to a small echo server. `path=direct` calls the server itself. `path=gate` calls it through a `derbent mcp` process with one allow rule, so each call takes the extra stdio hop through the gate, a rule decision and a receipt appended to SQLite before the answer comes back. After the run the benchmark checks that the database holds one receipt per call.
- `BenchmarkHook` runs one pre-tool hook call the way a CLI does: `call=hook` starts `derbent gate --agent claude` with a Claude `PreToolUse` input on stdin, which loads the config, opens the database, decides the call with an allow rule and writes a receipt. `call=spawn` starts the same binary as `derbent version`, which exits at once. That is the part of the cost any `derbent` invocation pays: starting this test binary, including its package initialisation. `call=hook-beside-gate` is `call=hook` while the benchmark holds a connection to the same database open for the whole run, as a running `derbent mcp` does. The `-wal` file is then always there, so each of these hook calls also checks the database on a read-only open first.

Calls are made one after another on one session, so calls per second is the inverse of the mean latency, not throughput under concurrent load. The binary the benchmarks start, in all three hook rows and as the gate, is the Go test binary of `cmd/derbent`, not a release build.

All the numbers come from one workflow run on shared GitHub-hosted runners. On each OS, all ten runs of each benchmark ran in one job on one VM, so the confidence intervals only cover noise within that VM. Another run, or another runner, may differ by more than the intervals shown, and runs of the same code have (see Reading the numbers). The Linux job ran on an `AMD EPYC 9V74 80-Core Processor` and the Windows job on an `AMD EPYC 7763 64-Core Processor`, as the raw files name them. Compare the gate with its baseline within each OS, not one OS with the other.

The p99 of one run is a single sample: index `n*99/100` of that run's sorted latencies. An echo run has 2000 samples, so its p99 is about the 20th largest. A hook run has only 200, so its p99 is about the second largest. Judge the hook by its p50. Three things in this run need care:

- On Linux the lone hook has a long tail again: its p99 was between 99.26 ms and 382.33 ms across the ten runs, and its mean between 12.51 ms and 28.87 ms, while its p50 stayed between 5.63 ms and 6.24 ms. The hook beside a gate has no such tail (p99 between 5.16 ms and 7.67 ms, mean between 4.79 ms and 4.86 ms), nor does the spawn. The same tail showed in run 36916385907 on an AMD EPYC 9V45, above 170 ms in seven of ten runs, and not in run 36919933154 on an Intel Xeon Platinum 8370C. Nothing has profiled it.
- On Linux the gate row of the echo benchmark had a mean between 0.84 ms and 1.93 ms across the ten runs, while its p50 stayed between 600 µs and 624 µs, which gives that row's p99 its ±58% and its calls per second ±37%.
- On Windows the lone hook's p50 moved during the job: between 72.63 ms and 75.43 ms in the first four runs, and between 104.03 ms and 115.32 ms in the last six, so its median of 105.66 ms has an interval of ±31%. Even the lower four sit well above the hook beside a gate, whose p50 stayed between 53.43 ms and 57.84 ms. One lone hook run had a p99 of 2.38 s.

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
| p50 | 201.0 µs ± 3% | 609.5 µs ± 1% |
| p99 | 488.5 µs ± 10% | 1248.5 µs ± 58% |
| Calls per second | 4234.0 ± 2% | 989.7 ± 37% |

| Hook call | Spawn baseline | `derbent gate`, allowed | `derbent gate` beside a running gate |
|-----------|----------------|-------------------------|--------------------------------------|
| p50 | 3.443 ms ± 1% | 5.812 ms ± 5% | 4.795 ms ± 0% |
| p99 | 3.772 ms ± 78% | 215.730 ms ± 73% | 5.420 ms ± 39% |
| Calls per second | 289.55 ± 2% | 63.44 ± 43% | 207.70 ± 0% |

## Windows

From `windows.txt`. Each value is the median of ten runs with its 95% confidence interval.

| MCP tool call | Direct | Through the gate |
|---------------|--------|------------------|
| p50 | 342.5 µs ± 3% | 1004.0 µs ± 4% |
| p99 | 910.0 µs ± 4% | 2163.5 µs ± 20% |
| Calls per second | 2442.5 ± 1% | 641.4 ± 10% |

| Hook call | Spawn baseline | `derbent gate`, allowed | `derbent gate` beside a running gate |
|-----------|----------------|-------------------------|--------------------------------------|
| p50 | 50.99 ms ± 10% | 105.66 ms ± 31% | 53.62 ms ± 5% |
| p99 | 71.34 ms ± 19% | 156.21 ms ± 118% | 71.88 ms ± 42% |
| Calls per second | 18.845 ± 13% | 9.101 ± 46% | 18.075 ± 7% |

## Reading the numbers

The differences below are between the medians in the tables above.

- An MCP call through the gate takes 408.5 µs longer at p50 than a direct call on Linux, and 661.5 µs longer on Windows.
- A lone hook call takes 2.369 ms longer at p50 than starting the binary and exiting on Linux, and 54.67 ms longer on Windows.
- A hook call beside a running gate takes 1.352 ms longer than the start on Linux and 2.63 ms longer on Windows, where benchstat does not tell it from the start (p=0.063). So with a gate running, almost all of a Windows hook call's cost is the process start: 50.99 ms of its 53.62 ms. A lone hook call costs 1.017 ms more than one beside a gate on Linux and 52.04 ms more on Windows, although the one beside a gate does an extra read-only open.
- The likely cause is unproven. A lone hook call holds the database's only connection, so when it closes, SQLite checkpoints the WAL into the database and deletes the `-wal` and `-shm` files; a hook call beside a running gate is not the last connection and does neither. Nothing here measures the close on its own.
- The run published before this one (run 36919933154, commit `592d7c8`) measured those differences at 499.5 µs and 616.0 µs for an MCP call, and at 2.129 ms and 24.11 ms for a lone hook call. Between the two commits the code on the lone hook's path did not change, so the Windows rise from 24.11 ms to 54.67 ms comes from the run and the machine: that run's Windows job had an AMD EPYC 9V74, this one an EPYC 7763. Runner CPUs differ from run to run (an EPYC 7763, 9V74 or 9V45, or an Intel Xeon Platinum 8370C so far), so read every number here as one runner's, not as a bound.
