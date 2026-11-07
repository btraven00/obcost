# obcost

Records what [omnibenchmark](https://github.com/omnibenchmark/omnibenchmark) runs
cost, per node: wall time, memory, CPU, I/O (snakemake's `performance.txt`) and
output size, plus the run around them (plan identity, host, exit code). Rows are
append-only JSONL; query them with DuckDB. Plans need `api_version` 0.8 or later.

```sh
go build -o obcost .
obcost ingest [-exit N] [-a key=value]... OUT_DIR   # an ob out dir
ob config hooks.post-run "obcost post-run"           # or after every ob run (design 016)
obcost import [-a key=value]... DIR                  # merge another store, e.g. a remote run's out/.hooks/
obcost ls [PLAN]                                     # runs, newest first
```

Run as ob's hook, obcost also records the run's slice (`OB_SELECTION`: `until`,
`module` or `filter`; `null` for a full run). For a remote run, the hook on the
remote machine writes into `$OB_HOOKS_OUT` (set `OBCOST_DIR` to it), the files
come back with the outputs, and `obcost import -a remote=… DIR` merges them.

`ls PLAN` keeps one benchmark's runs: PLAN is a plan file (matched by its
`provenance.canonical_url`, else its `id`), a URL, or a plan id. Matching by URL
groups forks and variants of a benchmark; it also means a changed
`canonical_url` starts a new group (`ls OLD_URL` still finds the old runs).

```
RUN       TIME              PLAN                     SLICE       EXIT  NODES  SECONDS  MAX_RSS_MB  ATTRS
5d0e7a11  2026-11-07T17:02  omni_thoa_example@0.1.0  until=DATA  0     1      31.2     138         remote=thoa
```

Rows go to `$OBCOST_DIR` (default `$XDG_DATA_HOME/obcost`): `runs.jsonl`, `nodes.jsonl`.
A run already recorded (same `run_id`) is skipped. `-a` attaches anything the
caller knows, e.g. `-a remote=thoa -a job=… -a cost_usd=…`.

```sql
-- per stage: runs, mean seconds, peak memory
select n.stage, count(*) runs, avg(n.perf.s) s, max(n.perf.max_rss) max_rss_mb
from read_json_auto('~/.local/share/obcost/nodes.jsonl') n
group by n.stage;

-- runs of one benchmark, across forks and variants (provenance.canonical_url)
select r.plan_version, r.exit_code, r.attrs, r.timestamp
from read_json_auto('~/.local/share/obcost/runs.jsonl') r
where r.plan_url = 'https://…/plan.yaml';
```
