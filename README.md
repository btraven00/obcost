# obcost

Records what [omnibenchmark](https://github.com/omnibenchmark/omnibenchmark) runs
cost, per node: wall time, memory, CPU, I/O (snakemake's `performance.txt`) and
output size, plus the run around them (plan identity, host, exit code). Rows are
append-only JSONL; query them with DuckDB. Plans need `api_version` 0.8 or later.

```sh
go build -o obcost .
obcost ingest [-exit N] [-a key=value]... OUT_DIR   # an ob out dir
ob config hooks.post-run "obcost post-run"           # or after every ob run (design 016)
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
