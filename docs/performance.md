# Performance

Costroid states four performance bars. Each has a make target that measures it and fails when the bar does not hold.

| Bar | Measured by | In CI |
| --- | ----------- | ----- |
| `costroid --version` exits in under 100 ms at p95, on two cores | `make bench-start` | the `bench-start` job |
| `costroid policy` exits in under 1 second at p95, on two cores | `make bench-start` | the `bench-start` job |
| The per-PR view, `GET /v1/pulls/{pr}`, answers in under 200 ms at p99 on one million rows | `make bench-view` | the `bench` job |
| `POST /v1/rows` takes 10,000 rows a second, one stream on one instance | `make bench-ingest` | no job |

CI holds the first three bars on every push to `main` and on every pull request, in the `bench-start` and `bench` jobs of `.github/workflows/ci.yml`. No CI job runs the ingest bar.

The readings below were taken on 2026-11-12, of the code at commit `d6e52fc`, on an AMD Ryzen AI MAX+ 395 with 32 cores under WSL2, with other work running on the machine. The load average beside each reading is the machine's, from `uptime`, as the reading started. These are not a hosted runner's cores. The CI job's log holds the runner's reading.

## Start time

`make bench-start` builds `bin/costroid` with the flags `make build` uses, the flags a release is built with. It runs `costroid --version` 30 times and `costroid policy` 30 times, each a fresh process, and times each process from start to exit. It prints the minimum, median, 95th percentile and maximum of each command, and fails when a 95th percentile reaches its bar.

`costroid policy` reads `internal/comment/testdata/rows.jsonl`, the 19 rows of a recorded Claude Code 2.1.316 run, with a `costroid.yml` that sets `run_budget_usd`, `max_run_cost_usd` and `models`.

The bars are stated for two cores. The `bench-start` job prints the runner's core count, runs the timing pinned to two of its cores with `taskset -c 0,1`, and fails when the pin does not give it two.

Last local reading, pinned with `taskset -c 0,1`, load average 4.7:

| Command | Min | Median | p95 | Max | Bar at p95 |
| ------- | --- | ------ | --- | --- | ---------- |
| `costroid --version` | 39.5 ms | 51.4 ms | 59.9 ms | 74.0 ms | 100 ms |
| `costroid policy` | 28.4 ms | 33.0 ms | 43.5 ms | 50.3 ms | 1 second |

`costroid --version` loads the embedded catalog to name its version and the day its prices were read. `costroid policy` loads no catalog.

## The per-PR view

`make bench-view` starts Postgres in Docker and writes one million rows for one tenant and 100,000 for a second, so row level security has rows to exclude. It then sends 3,000 requests for pull requests sampled by their row count, over HTTP, with key authentication. It prints the p50 and p99 and fails when the p99 reaches 200 ms. `make check` ends with it.

Last local reading, on all 32 cores, load average 19.4 as `make check` started and 11.3 as it ended: p50 12.6 ms, p99 34.8 ms, max 297.3 ms, over 3,000 requests. The heaviest pull request held 1,888 rows.

## Ingest

`make bench-ingest` starts Postgres in Docker and posts 10 batches of 10,000 rows through `POST /v1/rows`, one after another: token check, parse, price and one batched write. Go runs with `GOMAXPROCS` set to `INGEST_CORES`, 8 by default. The benchmark fails when that one stream writes under 10,000 rows a second. It also prints the rate of four streams at once, which nothing holds to a floor.

The bar names no core count. This page reads "one instance" as one `costroid serve` process with 8 cores, the default `make bench-ingest` has measured since its first figures in [`serve.md`](serve.md#throughput).

Last local readings, 8 cores for the service, Postgres 17 in Docker on the same machine with `fsync=off`:

| Load average | One stream, rows/s | Four streams, rows/s | Floor |
| ------------ | ------------------ | -------------------- | ----- |
| 11.1 | 9,656 | 21,512 | not held |
| 8.3 | 11,221 | 20,798 | held |
| 5.9 | 9,988 | 17,600 | not held |
| 11.6 | 9,722 | 22,997 | not held |

Three of the four readings are under the bar. The bar is not moved.
