# Performance

## Method

Benchmarks are deterministic package benchmarks and exclude server/network
latency. Run them with:

```sh
make benchmark
```

For lower noise, use `-benchtime=5x -count=5` and compare distributions with
`benchstat`. The numbers below are a development baseline, not portable release
thresholds.

## Baseline

Measured 2026-09-07 on macOS arm64, Apple M1 Max, Go 1.23 toolchain, one
iteration per benchmark, with `CGO_ENABLED=0`:

| Benchmark | Time | Throughput | Allocated | Allocations |
| --- | ---: | ---: | ---: | ---: |
| Delta apply, 100 KiB | 69.5 us | 1474 MiB/s | 104 KiB | 1 |
| Delta generation, 100 KiB repeated input | 3.91 ms | 26.2 MiB/s | 3.19 MiB | 293 |
| FSFS revision root | 251 us | - | 200 KiB | 56 |
| ra_svn tuple marshal | 113 us | - | 7.98 KiB | 15 |
| ra_svn tuple unmarshal | 37.2 us | 25.0 MiB/s | 5.84 KiB | 15 |
| WC checkout, 10,000 files | 11.1 s | - | 721 MiB | 7,752,930 |

## Findings

Repeated source data previously created unbounded rolling-hash candidate lists
in delta generation. Candidate evaluation is now capped with an early exit for
good matches, improving the adversarial case from roughly 0.43 MiB/s to 26
MiB/s while preserving compact deltas.

WC checkout profiling showed SQLite autocommit and filesystem syscalls dominated
runtime. An update-wide SQLite transaction reduced the 10,000-file checkout
from roughly 33 seconds and 1.02 GB allocated to 11 seconds and 721 MB. Nested
atomic helpers use savepoints so update rollback remains all-or-nothing.

The checkout benchmark remains syscall-heavy and is the clearest optimization
target. Future work should be profile-driven: prepared statement caching and
bounded work-queue batching are candidates, but must preserve WC transaction and
crash-recovery semantics.