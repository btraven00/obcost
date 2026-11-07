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
RUN       TIME              PLAN                     SLICE         EXIT  NODES  SECONDS  MAX_RSS_MB  ATTRS
d8782049  2026-11-07T16:31  omni_thoa_example@0.1.0  until=REPORT  0     0      0.0      0           billed_cost_usd=0.027611855744 credits_debited=3 job=eede5824-… remote=thoa
51396bac  2026-11-07T16:22  omni_thoa_example@0.1.0  until=FILT    0     1      16.1     3135        billed_cost_usd=0.043865181998 credits_debited=5 job=5627cbb4-… remote=thoa
3a47fadc  2026-11-07T16:16  omni_thoa_example@0.1.0  until=DATA    0     1      26.3     138         billed_cost_usd=0.032107879039 credits_debited=4 job=d740d765-… remote=thoa
```

Three slices of a plan run as thoa jobs (omni-thoa-example's `thoa/slices.py`): obcost ran
as ob's post-run hook on each VM, and the driver imported the rows with thoa's cost.
REPORT has no nodes because ob's gather rules write no `performance.txt`.

```
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
