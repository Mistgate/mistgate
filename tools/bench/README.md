# Benchmark harness

The scripts, load generators and compose files behind [Benchmarks](../../docs/en/reference/benchmarks.md) ([ru](../../docs/ru/reference/benchmarks.md)): Mistgate v0.1.32, 3x-ui v3.9.0, Remnawave 3.4.5 and PasarGuard v5.4.1 measured on the same small server, panel only and as a one-server setup with the VPN core and Hysteria2 traffic. All data is invented (`user1`..`userN`, `node1.bench.test`, `203.0.113.x`); every service listens on loopback or on an isolated Docker network.

**Read this before you run anything.** The harness changes the machine it runs on. It writes systemd slices into `/etc/systemd/system`, imports and pulls Docker images (about 2.5 GB), creates the Docker networks `bench-iso` and `bench-box`, adds about 1,200 alias addresses on the `bench-box` bridge, starts and kills containers and `systemd-run` units named `bench*`, drops the page cache when you pass `--dropcaches` and, with `--pin`, changes CPU affinity. Run it on a dedicated test machine or a throwaway VM, as root. `cleanup.sh` removes what the harness created, including the images by name and `$BENCH_ROOT`; read it first if the machine runs anything else.

## Requirements

- Linux with systemd and cgroup v2 (the runs were made in WSL2, Ubuntu 24.04, kernel 6.6), root, Docker with Compose v2, Python 3, Go 1.26+ (for `hyload`), `openssl`, `iproute2`, `taskset`, `curl`.
- Spare capacity beside the limited slices: the clients run outside them and use 150-250 % of one CPU in the one-server test. The reference machine had 16 threads and 15.5 GB of RAM; on a smaller one the clients compete with the measured group.
- The release under test: `mistgate` and `mistgate-node` (the runs used a build from the v0.1.32 tag without a release key), plus the `.env.sample` of the Remnawave release and the `env.example` of the PasarGuard release named at the top of `compose/remna.yml` and `compose/pasar.yml`.

## Layout

| Path | What |
|:--|:--|
| `py/bench.py` | panel-only harness: cold start, idle, 200 users through the API, subscription load, restart. One slice, 1 vCPU / 1 GB |
| `py/onebox.py` | one-server harness: panel, core and database in one slice (2 vCPU / 2 GB by default), phases A to E, Hysteria2 |
| `py/aggregate.py`, `aggregate_box.py`, `merge_box.py` | median and spread tables from the raw JSON of a run |
| `py/profile_run.py`, `pprof_report.py` | the Mistgate profiling pass (`go tool pprof -top` listings) |
| `py/tail_diag.py` | where the p99 tail of the uncached subscription path comes from (`GOMAXPROCS`) |
| `py/*_probe.py`, `mg_boot.py`, `pg_user2.py`, `peek.py`, `bench_main_used.py` | API exploration scripts and the exact `bench.py` version of the main run |
| `loadgen/` | subscription load generator (Go, standard library only) |
| `hyload/` | Hysteria2 tools on the official client library: `sink`, `tp` (throughput and a probe request), `many` (N clients) |
| `compose/` | the compose files as used, with the changes from the projects' own files noted at the top of each |
| `mistgate-bench-variants.diff` | the two-line changes of the supplementary "limits off" builds. Never the shipped build |
| `profiling/` | the `net/http/pprof` overlay files and `mkoverlay.py` for `go build -overlay` |
| `results/` | the aggregated tables and per-repetition CSVs the page is built from. The raw JSON and the 1 Hz samples are not published |

## Run

`BENCH_ROOT` is the working directory of the harness (default `/root/bench`); compose files and scripts read it from the environment.

```sh
export BENCH_ROOT=/root/bench
./setup.sh ./mistgate ./mistgate-node ./remnawave.env.sample ./pasarguard.env.example
cd "$BENCH_ROOT"
```

`setup.sh` creates `bench-panel.slice` (`CPUQuota=100%`, `MemoryMax=1G`, no swap), copies the harness, builds `loadgen` and `hyload`, and writes the two `.env` files with random secrets.

Panel only, as in the main series (the group was also pinned to one host CPU, the last one; see the page for why and for the unpinned comparison):

```sh
systemctl set-property --runtime bench-panel.slice AllowedCPUs=15   # the last of 16 threads; adjust to your CPU count
python3 bench.py --panels mistgate,3xui,remnawave,pasarguard --reps 3 --idle 300 --load 60 --users 200 --conc 50 --tag main
```

The `mistgate-nolimit` and `mistgate-nolimit-nocache` panels need those binaries in `$BENCH_ROOT/bin/`: build them from the v0.1.32 tag with `mistgate-bench-variants.diff` applied (the comment at the top of the diff says which file gets which change).

One server, 2 vCPU / 2 GB, 1000 users (phases A idle, B throughput, C many clients, D bad day, E panel restart):

```sh
python3 onebox.py --panels mistgate,3xui,remnawave,pasarguard --reps 3 --users 1000 --phases ABCDE --bad-dur 120 --storm-rate 300 --tag box
python3 onebox.py --panels mistgate,3xui,remnawave,pasarguard --reps 1 --users 1000 --vcpu 1 --mem 1G --phases ABDE --settle 30 --idle 90 --bad-dur 120 --storm-rate 300 --tag box1g
python3 onebox.py --panels ... --reps 1 --users 1000 --pin 14-15 --phases BDE --tag box_pin2   # pinned check; clients go to CPUs 0-13
```

The CPU numbers in `--pin`, in `AllowedCPUs=15` and in the `taskset` calls of the scripts assume a 16-thread host; change them for yours. Results land in `$BENCH_ROOT/out/<tag>/`: one JSON and one `*_samples.csv` per panel and repetition. Turn them into tables with:

```sh
python3 aggregate.py   out/main          # panel only
python3 aggregate_box.py out/box         # one server
```

Absolute numbers depend on the CPU, the kernel and the filesystem; compare the panels with each other, not with the page's absolute values.

## Profiling pass

`profiling/mkoverlay.py` writes `go build -overlay` files that add `net/http/pprof` to the panel and the node without touching the source tree. Build `mistgate-prof`, `mistgate-node-prof` and `mistgate-nolimit-nocache-prof`, put them in `$BENCH_ROOT/bin/`, then run `py/profile_run.py --phases idle,storm,nocache,box --out "$BENCH_ROOT/out/profiles"` and `py/pprof_report.py <dir>`. The findings are in `results/profiling-summary.txt`.

## Cleanup

```sh
cp -r "$BENCH_ROOT/out" ~/bench-out   # keep the results
./cleanup.sh
```
