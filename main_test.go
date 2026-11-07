package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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

func TestList(t *testing.T) {
	t.Setenv("OBCOST_DIR", t.TempDir())
	t.Setenv("OB_PLAN_ID", "")
	for _, p := range []struct{ run, id, url string }{{"aaaa1111", "a", "https://x/a.yaml"}, {"bbbb2222", "b", ""}} {
		out := t.TempDir()
		write(t, filepath.Join(out, ".metadata/manifest.json"), `{"run_id":"`+p.run+`"}`)
		write(t, filepath.Join(out, ".metadata/benchmark.yaml"), "id: "+p.id+"\napi_version: 0.8.0\nprovenance:\n  canonical_url: "+p.url+"\n")
		write(t, filepath.Join(out, "S.m.x/performance.txt"), perf)
		if err := record(out, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	plan := filepath.Join(t.TempDir(), "plan.yaml") // a fork of a: other id, same canonical_url
	write(t, plan, "id: a-fork\nprovenance:\n  canonical_url: https://x/a.yaml\n")
	for sel, want := range map[string]string{"": "aaaa1111 bbbb2222", plan: "aaaa1111", "https://x/a.yaml": "aaaa1111", "b": "bbbb2222"} {
		r, w, _ := os.Pipe()
		stdout := os.Stdout
		os.Stdout = w
		err := list(sel)
		w.Close()
		os.Stdout = stdout
		b := make([]byte, 4096)
		n, _ := r.Read(b)
		var got []string
		for _, l := range strings.Split(string(b[:n]), "\n")[1:] {
			if f := strings.Fields(l); len(f) > 0 {
				got = append(got, f[0])
			}
		}
		slices.Sort(got)
		if err != nil || strings.Join(got, " ") != want {
			t.Errorf("ls %q = %v, %v; want %s", sel, got, err, want)
		}
	}
}

func TestImportRemoteStore(t *testing.T) {
	out := t.TempDir()
	write(t, filepath.Join(out, ".metadata/manifest.json"), `{"run_id":"remote01"}`)
	write(t, filepath.Join(out, ".metadata/benchmark.yaml"), "id: p\napi_version: 0.8.0\n")
	write(t, filepath.Join(out, "DATA.m.x/performance.txt"), perf)
	// on the VM: obcost as ob's post-run hook, into the out dir's .hooks/
	remote := filepath.Join(out, ".hooks")
	t.Setenv("OBCOST_DIR", remote)
	t.Setenv("OB_HOOK", "post-run")
	t.Setenv("OB_SELECTION", `{"until": "DATA"}`)
	code := 0
	if err := record(out, &code, nil); err != nil {
		t.Fatal(err)
	}
	// locally: import it, attaching what the driver knows
	t.Setenv("OB_HOOK", "")
	t.Setenv("OBCOST_DIR", t.TempDir())
	for range 2 {
		if err := importStore(remote, map[string]string{"remote": "thoa", "cost_usd": "0.02"}); err != nil {
			t.Fatal(err)
		}
	}
	runs, _ := readJSONL[Run](filepath.Join(store(), "runs.jsonl"))
	nodes, _ := readJSONL[Node](filepath.Join(store(), "nodes.jsonl"))
	if len(runs) != 1 || len(nodes) != 1 || runs[0].Attributes["remote"] != "thoa" || slice(runs[0].Selection) != "until=DATA" {
		t.Fatalf("runs %+v nodes %d", runs, len(nodes))
	}
	if slice(nil) != "?" || slice(json.RawMessage("null")) != "full" {
		t.Error("slice rendering")
	}
}
