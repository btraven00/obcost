package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

const perf = "s\th:m:s\tmax_rss\tcpu_time\n31.2\t0:00:31\t138.2\t1.66\n"

func TestRecord(t *testing.T) {
	out := t.TempDir()
	t.Setenv("OBCOST_DIR", t.TempDir())
	t.Setenv("OB_PLAN_ID", "") // from the plan copy, not a hook env
	write(t, filepath.Join(out, ".metadata/manifest.json"), `{"run_id":"r1","ob_version":"0.8","cpu_count":4}`)
	write(t, filepath.Join(out, ".metadata/benchmark.yaml"),
		"id: omni_thoa_example\nversion: \"0.1.0\"\napi_version: \"0.8.0\"\nprovenance:\n  canonical_url: https://x/plan.yaml # canonical\nstages:\n  - id: DATA\n")
	write(t, filepath.Join(out, "DATA.be1.44d965bf/performance.txt"), perf)
	write(t, filepath.Join(out, "DATA.be1.44d965bf/be1.h5ad"), "12345")
	write(t, filepath.Join(out, "DATA.be1.44d965bf/FILT.fi_scrapper.default/performance.txt"), perf)
	write(t, filepath.Join(out, ".snakemake/conda/x/performance.txt"), perf) // not a result
	write(t, filepath.Join(out, "human/DATA.be1.x/performance.txt"), perf)   // ob's view of the same node

	r, nodes, err := ingest(out, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if r.PlanID != "omni_thoa_example" || r.PlanVer != "0.1.0" || r.PlanURL != "https://x/plan.yaml" {
		t.Errorf("plan identity %q %q %q", r.PlanID, r.PlanVer, r.PlanURL)
	}
	if len(nodes) != 2 {
		t.Fatalf("nodes %+v", nodes)
	}
	d, f := nodes[0], nodes[1]
	if d.Stage != "DATA" || d.Module != "be1" || d.Bytes != 5 || d.Perf["max_rss"] != 138.2 || d.Perf["s"] != 31.2 {
		t.Errorf("DATA node %+v", d)
	}
	if _, ok := d.Perf["h:m:s"]; ok {
		t.Error("h:m:s should be skipped")
	}
	if f.Node != "DATA.be1.44d965bf/FILT.fi_scrapper.default" || f.Stage != "FILT" || f.Bytes != 0 {
		t.Errorf("FILT node %+v", f)
	}

	code := 0
	for range 2 { // a second ingest of the same run adds nothing
		if err := record(out, &code, map[string]string{"remote": "thoa"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := os.ReadFile(filepath.Join(store(), "runs.jsonl"))
	nodesB, _ := os.ReadFile(filepath.Join(store(), "nodes.jsonl"))
	if n := strings.Count(string(runs), "\n"); n != 1 || !strings.Contains(string(runs), `"remote":"thoa"`) {
		t.Errorf("runs.jsonl (%d rows): %s", n, runs)
	}
	if n := strings.Count(string(nodesB), "\n"); n != 2 {
		t.Errorf("nodes.jsonl has %d rows", n)
	}
}

func TestRefusesOldAPI(t *testing.T) {
	t.Setenv("OBCOST_DIR", t.TempDir())
	for _, api := range []string{"", `api_version: "0.7.0"\n`} {
		out := t.TempDir()
		write(t, filepath.Join(out, ".metadata/manifest.json"), `{"run_id":"r1"}`)
		write(t, filepath.Join(out, ".metadata/benchmark.yaml"), "id: x\n"+api)
		if err := record(out, nil, nil); err == nil || !strings.Contains(err.Error(), "0.8") {
			t.Errorf("api %q: err %v", api, err)
		}
	}
	if _, err := os.Stat(filepath.Join(store(), "runs.jsonl")); err == nil {
		t.Error("a refused run wrote rows")
	}
	for v, want := range map[string]bool{"0.8.0": true, "0.10.0": true, "1.0": true, "0.7.9": false, "": false, "x.y": false} {
		if atLeast(v, 0, 8) != want {
			t.Errorf("atLeast(%q) != %v", v, want)
		}
	}
}
