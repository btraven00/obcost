// Command obcost records what omnibenchmark runs cost: wall time, memory and
// output size per node, and the run around them, as append-only JSONL that
// DuckDB (or jq) reads directly.
//
//	obcost ingest [-exit N] [-a key=value]... OUT_DIR
//	obcost post-run PLAN OUT_DIR EXIT        as ob's post-run hook (design 016)
//
// Rows go to $OBCOST_DIR (default $XDG_DATA_HOME/obcost): runs.jsonl and
// nodes.jsonl. A run already ingested (same run_id) is skipped.
package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Run struct {
	RunID      string            `json:"run_id"`
	Timestamp  string            `json:"timestamp"`
	Ingested   string            `json:"ingested_at"`
	OBVersion  string            `json:"ob_version"`
	Host       string            `json:"hostname"`
	CPUs       int               `json:"cpu_count"`
	MemMB      int               `json:"memory_total_mb"`
	PlanURL    string            `json:"plan_url"`
	PlanID     string            `json:"plan_id"`
	PlanVer    string            `json:"plan_version"`
	API        string            `json:"api_version"`
	OutDir     string            `json:"out_dir"`
	ExitCode   *int              `json:"exit_code"`
	Nodes      int               `json:"nodes"`
	Attributes map[string]string `json:"attrs,omitempty"`
}

type Node struct {
	RunID  string             `json:"run_id"`
	Node   string             `json:"node"` // dir under out/, e.g. DATA.be1.44d965bf/FILT.fi_scrapper.default
	Stage  string             `json:"stage"`
	Module string             `json:"module"`
	Bytes  int64              `json:"output_bytes"` // files in the node's own dir
	Perf   map[string]float64 `json:"perf"`         // snakemake benchmark: s, max_rss (MB), io_in, cpu_time, ...
}

// skip holds out/ entries that aren't results; human/ is ob's readable view of
// the same nodes (links), which would count them twice.
var skip = map[string]bool{".snakemake": true, ".modules": true, ".hooks": true, ".logs": true, ".metadata": true, "human": true}

func ingest(out string, exit *int, attrs map[string]string) (*Run, []Node, error) {
	var man struct {
		RunID     string `json:"run_id"`
		Timestamp string `json:"timestamp"`
		OBVersion string `json:"ob_version"`
		Host      string `json:"hostname"`
		CPUs      int    `json:"cpu_count"`
		MemMB     int    `json:"memory_total_mb"`
	}
	b, err := os.ReadFile(filepath.Join(out, ".metadata", "manifest.json"))
	if err != nil {
		return nil, nil, fmt.Errorf("%s: not an ob out dir: %w", out, err)
	}
	if err := json.Unmarshal(b, &man); err != nil {
		return nil, nil, err
	}
	abs, _ := filepath.Abs(out)
	r := &Run{RunID: man.RunID, Timestamp: man.Timestamp, OBVersion: man.OBVersion, Host: man.Host,
		CPUs: man.CPUs, MemMB: man.MemMB, OutDir: abs, ExitCode: exit, Attributes: attrs,
		Ingested: time.Now().UTC().Format(time.RFC3339)}
	r.PlanID, r.PlanVer, r.PlanURL, r.API = planIdentity(out)
	// Before 0.8 ob laid outputs out as <stage>/<module>/.<params>/, which the
	// node names below don't read; no api_version is the legacy 0.4 contract.
	if !atLeast(r.API, 0, 8) {
		return nil, nil, fmt.Errorf("%s: plan api_version %q: obcost needs 0.8 or later", out, r.API)
	}

	var nodes []Node
	err = filepath.WalkDir(out, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && filepath.Dir(p) == filepath.Clean(out) && skip[d.Name()] {
			return fs.SkipDir
		}
		if d.Name() != "performance.txt" {
			return nil
		}
		n, err := node(out, filepath.Dir(p))
		if err == nil {
			n.RunID = r.RunID
			nodes = append(nodes, n)
		}
		return err
	})
	slices.SortFunc(nodes, func(a, b Node) int { return strings.Compare(a.Node, b.Node) })
	r.Nodes = len(nodes)
	return r, nodes, err
}

// node reads one node dir: its performance.txt and the size of its files.
func node(out, dir string) (Node, error) {
	rel, _ := filepath.Rel(out, dir)
	n := Node{Node: filepath.ToSlash(rel), Perf: map[string]float64{}}
	// ob 0.8 layout: <STAGE>.<module>.<params>, nested per lineage
	if f := strings.SplitN(filepath.Base(dir), ".", 3); len(f) == 3 {
		n.Stage, n.Module = f[0], f[1]
	}
	b, err := os.ReadFile(filepath.Join(dir, "performance.txt"))
	if err != nil {
		return n, err
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) >= 2 {
		head, vals := strings.Split(lines[0], "\t"), strings.Split(lines[1], "\t")
		for i := 0; i < len(head) && i < len(vals); i++ {
			if v, err := strconv.ParseFloat(vals[i], 64); err == nil {
				n.Perf[head[i]] = v // skips h:m:s and NA
			}
		}
	}
	ents, err := os.ReadDir(dir)
	for _, e := range ents {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() && e.Name() != "performance.txt" {
			n.Bytes += info.Size()
		}
	}
	return n, err
}

// atLeast reports whether version v ("0.8.0") is at least major.minor.
func atLeast(v string, major, minor int) bool {
	f := strings.SplitN(v, ".", 3)
	if len(f) < 2 {
		return false
	}
	ma, err1 := strconv.Atoi(f[0])
	mi, err2 := strconv.Atoi(f[1])
	return err1 == nil && err2 == nil && (ma > major || ma == major && mi >= minor)
}

// planIdentity reads ob's plan copy for the plan's id, version,
// provenance.canonical_url and api_version; the hook env (OB_PLAN_*) wins
// for the first three when ob runs us.
// ponytail: a line scan of top-level keys, not a YAML parser; enough for ob's plans.
func planIdentity(out string) (id, ver, url, api string) {
	defer func() {
		if e := os.Getenv("OB_PLAN_ID"); e != "" {
			id, ver, url = e, os.Getenv("OB_PLAN_VERSION"), os.Getenv("OB_PLAN_URL")
		}
	}()
	f, err := os.Open(filepath.Join(out, ".metadata", "benchmark.yaml"))
	if err != nil {
		return
	}
	defer f.Close()
	val := func(l, k string) (string, bool) {
		v, ok := strings.CutPrefix(l, k)
		return strings.Trim(strings.TrimSpace(strings.SplitN(v, " #", 2)[0]), `"'`), ok
	}
	for s := bufio.NewScanner(f); s.Scan(); {
		l := s.Text()
		if v, ok := val(l, "api_version:"); ok {
			api = v
		} else if v, ok := val(l, "id:"); ok {
			id = v
		} else if v, ok := val(l, "version:"); ok {
			ver = v
		} else if v, ok := val(strings.TrimSpace(l), "canonical_url:"); ok {
			url = v
		}
	}
	return
}

func store() string {
	if d := os.Getenv("OBCOST_DIR"); d != "" {
		return d
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "obcost")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "obcost")
}

// seen reports whether runs.jsonl already has runID.
func seen(dir, runID string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "runs.jsonl"))
	return err == nil && strings.Contains(string(b), `"run_id":"`+runID+`"`)
}

func appendJSONL(path string, rows ...any) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}

// record ingests out and appends it; nodes first, so a crash between the two
// leaves the run un-seen and a retry completes it (readers dedupe nodes by run).
func record(out string, exit *int, attrs map[string]string) error {
	r, nodes, err := ingest(out, exit, attrs)
	if err != nil {
		return err
	}
	if r.RunID == "" {
		return errors.New(out + ": manifest has no run_id")
	}
	dir := store()
	if seen(dir, r.RunID) {
		fmt.Fprintf(os.Stderr, "obcost: run %s already recorded\n", r.RunID)
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	rows := make([]any, len(nodes))
	for i := range nodes {
		rows[i] = nodes[i]
	}
	if err := appendJSONL(filepath.Join(dir, "nodes.jsonl"), rows...); err != nil {
		return err
	}
	if err := appendJSONL(filepath.Join(dir, "runs.jsonl"), r); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "obcost: run %s, %d nodes -> %s\n", r.RunID, len(nodes), dir)
	return nil
}

type attrFlag map[string]string

func (a attrFlag) String() string { return fmt.Sprint(map[string]string(a)) }
func (a attrFlag) Set(v string) error {
	k, val, ok := strings.Cut(v, "=")
	if !ok {
		return fmt.Errorf("want key=value, got %q", v)
	}
	a[k] = val
	return nil
}

const usage = `usage:
  obcost ingest [-exit N] [-a key=value]... OUT_DIR   record an ob run
  obcost post-run PLAN OUT_DIR EXIT                   the same, as ob's post-run hook:
                                                      ob config hooks.post-run "obcost post-run"
rows: $OBCOST_DIR (default $XDG_DATA_HOME/obcost)/{runs,nodes}.jsonl
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		fl := flag.NewFlagSet("ingest", flag.ExitOnError)
		exit := fl.Int("exit", -1, "the run's exit code, if known")
		attrs := attrFlag{}
		fl.Var(attrs, "a", "attribute key=value for the run (repeatable), e.g. remote=thoa")
		fl.Parse(os.Args[2:])
		if fl.NArg() != 1 {
			err = errors.New("ingest: need OUT_DIR")
			break
		}
		var e *int
		if *exit >= 0 {
			e = exit
		}
		err = record(fl.Arg(0), e, attrs)
	case "post-run":
		if len(os.Args) != 5 {
			err = errors.New("post-run: need PLAN OUT_DIR EXIT")
			break
		}
		code, cerr := strconv.Atoi(os.Args[4])
		if cerr != nil {
			err = cerr
			break
		}
		err = record(os.Args[3], &code, nil)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "obcost:", err)
		os.Exit(1)
	}
}
