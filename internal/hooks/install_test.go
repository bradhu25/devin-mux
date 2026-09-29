package hooks

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The user's actual config shape on 2026-09-28 (org id redacted).
const userConfig = `{
  "version": 1,
  "devin": {
    "org_id": "org-REDACTED"
  },
  "shell": {
    "setup_complete": true
  },
  "theme_mode": "dark",
  "agent": {
    "model": "claude-fable-5-1-high",
    "preferred_family_models": {
      "claude-5-fable": "claude-5-fable-low"
    }
  }
}
`

func parse(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, b)
	}
	return m
}

func TestMerge_AddsHooksPreservingEverythingElse(t *testing.T) {
	out, res, err := Merge([]byte(userConfig), "/usr/local/bin/dmux")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != len(Events) || res.Updated != nil || res.Kept != nil || !res.Changed() {
		t.Fatalf("result: %+v", res)
	}
	got := parse(t, out)
	want := parse(t, []byte(userConfig))
	for k, v := range want {
		if !reflect.DeepEqual(got[k], v) {
			t.Errorf("key %q changed: %v -> %v", k, v, got[k])
		}
	}
	hooks := got["hooks"].(map[string]any)
	for _, ev := range Events {
		entries := hooks[ev].([]any)
		if len(entries) != 1 {
			t.Fatalf("%s: %d entries", ev, len(entries))
		}
		e := entries[0].(map[string]any)
		h := e["hooks"].([]any)[0].(map[string]any)
		if e["matcher"] != "" || h["type"] != "command" || h["command"] != "/usr/local/bin/dmux hook-event" || h["timeout"] != float64(HookTimeoutSeconds) {
			t.Fatalf("%s entry: %v", ev, e)
		}
	}
	// Top-level key order preserved, hooks appended last.
	keys := keyOrder(out)
	if strings.Join(keys, ",") != "version,devin,shell,theme_mode,agent,hooks" {
		t.Fatalf("key order: %v", keys)
	}
}

func TestMerge_Idempotent(t *testing.T) {
	once, _, _ := Merge([]byte(userConfig), "/usr/local/bin/dmux")
	twice, res, err := Merge(once, "/usr/local/bin/dmux")
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed() || len(res.Kept) != len(Events) {
		t.Fatalf("second merge should change nothing: %+v", res)
	}
	if string(once) != string(twice) {
		t.Fatalf("output differs on second merge:\n%s\n---\n%s", once, twice)
	}
}

func TestMerge_ReplacesStaleBinaryPath(t *testing.T) {
	old, _, _ := Merge([]byte(userConfig), "/old/path/dmux")
	out, res, err := Merge(old, "/new/path/dmux")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Updated) != len(Events) || len(res.Added) != 0 {
		t.Fatalf("result: %+v", res)
	}
	if strings.Contains(string(out), "/old/path/dmux") || strings.Count(string(out), "/new/path/dmux hook-event") != len(Events) {
		t.Fatalf("stale path not replaced:\n%s", out)
	}
}

func TestMerge_NeverTouchesForeignHooks(t *testing.T) {
	cfg := `{"hooks":{"PreToolUse":[{"matcher":"exec","hooks":[{"type":"command","command":"./scripts/audit.sh"}]},{"matcher":"","hooks":[{"type":"command","command":"/x/other hook-event"},{"type":"command","command":"second"}]}],"Stop":[{"matcher":"","hooks":[{"type":"prompt","prompt":"remind"}]}]},"other":1}`
	out, res, err := Merge([]byte(cfg), "/usr/local/bin/dmux")
	if err != nil {
		t.Fatal(err)
	}
	got := parse(t, out)
	pre := got["hooks"].(map[string]any)["PreToolUse"].([]any)
	if len(pre) != 3 {
		t.Fatalf("PreToolUse should keep 2 foreign entries + add 1: %v", pre)
	}
	s, _ := json.Marshal(pre[0])
	if !strings.Contains(string(s), "audit.sh") {
		t.Fatalf("foreign matcher entry altered: %s", s)
	}
	s, _ = json.Marshal(pre[1])
	if !strings.Contains(string(s), `"second"`) {
		t.Fatalf("multi-hook entry (not ours even though one ends in hook-event) altered: %s", s)
	}
	stop := got["hooks"].(map[string]any)["Stop"].([]any)
	if len(stop) != 2 {
		t.Fatalf("Stop should keep the prompt hook and add ours: %v", stop)
	}
	if got["other"] != float64(1) || len(res.Added) != len(Events) {
		t.Fatalf("other key or result wrong: %v %+v", got["other"], res)
	}
}

func TestMerge_EmptyAndInvalid(t *testing.T) {
	out, res, err := Merge(nil, "/b/dmux")
	if err != nil || len(res.Added) != len(Events) {
		t.Fatalf("empty config: %v %+v", err, res)
	}
	parse(t, out)
	if _, _, err := Merge([]byte(`[1,2]`), "/b/dmux"); err == nil {
		t.Fatal("non-object config must error")
	}
	if _, _, err := Merge([]byte(`{"hooks": "nope"}`), "/b/dmux"); err == nil {
		t.Fatal("non-object hooks must error")
	}
	// "hooks": null is treated as absent.
	if _, res, err := Merge([]byte(`{"hooks": null}`), "/b/dmux"); err != nil || len(res.Added) != len(Events) {
		t.Fatalf("null hooks: %v %+v", err, res)
	}
}

func TestInstall_BackupAtomicIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(userConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	res, backup, err := Install(path, "/usr/local/bin/dmux")
	if err != nil || !res.Changed() || backup == "" {
		t.Fatalf("%v %+v %q", err, res, backup)
	}
	if b, _ := os.ReadFile(backup); string(b) != userConfig {
		t.Fatal("backup must be the original bytes")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("perm %o", info.Mode().Perm())
	}
	// Second install: no change, no new backup, no temp files.
	res, backup, err = Install(path, "/usr/local/bin/dmux")
	if err != nil || res.Changed() || backup != "" {
		t.Fatalf("second install: %v %+v %q", err, res, backup)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
	// Missing file: created, no backup.
	fresh := filepath.Join(dir, "sub", "config.json")
	res, backup, err = Install(fresh, "/usr/local/bin/dmux")
	if err != nil || !res.Changed() || backup != "" {
		t.Fatalf("fresh install: %v %+v %q", err, res, backup)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatal("config should be created")
	}
}
