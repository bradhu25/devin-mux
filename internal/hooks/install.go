package hooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events dmux subscribes to. PostCompaction is omitted (no status value);
// unknown events would be recorded as raw anyway.
var Events = []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Stop", "SessionEnd"}

// HookTimeoutSeconds is generous relative to the ~10ms the hook takes, but
// bounds Devin's wait if the machine is under heavy load.
const HookTimeoutSeconds = 5

// DefaultConfigPath is Devin CLI's user-level config file.
func DefaultConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "devin", "config.json"), nil
}

// Command returns the hook command line for a given dmux binary path.
func Command(dmuxBin string) string { return dmuxBin + " hook-event" }

// MergeResult reports what Merge did.
type MergeResult struct {
	Added   []string // events where a dmux hook was added
	Updated []string // events where a stale dmux hook (different binary path) was replaced
	Kept    []string // events already correct
}

// Changed reports whether the config needs writing.
func (r MergeResult) Changed() bool { return len(r.Added)+len(r.Updated) > 0 }

// Merge returns config with dmux's hooks present for every event, preserving
// all other content and ordering of top-level keys. It is idempotent: a
// second call with the same binary path changes nothing. Existing dmux
// entries pointing at a different binary path are replaced (upgrade/move).
// Hooks belonging to anyone else are never touched.
func Merge(config []byte, dmuxBin string) ([]byte, MergeResult, error) {
	var res MergeResult
	root := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(config))) > 0 {
		if err := json.Unmarshal(config, &root); err != nil {
			return nil, res, fmt.Errorf("config is not a JSON object: %w", err)
		}
	}

	hooks := map[string][]json.RawMessage{}
	if raw, ok := root["hooks"]; ok && len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &hooks); err != nil {
			return nil, res, fmt.Errorf(`"hooks" is not an object of event -> array: %w`, err)
		}
	}

	want := Command(dmuxBin)
	for _, event := range Events {
		entries := hooks[event]
		found, stale := false, -1
		for i, e := range entries {
			cmd, isDmux := dmuxHookCommand(e)
			if !isDmux {
				continue
			}
			if cmd == want {
				found = true
			} else {
				stale = i
			}
		}
		switch {
		case found && stale < 0:
			res.Kept = append(res.Kept, event)
			continue
		case found && stale >= 0:
			entries = append(entries[:stale], entries[stale+1:]...)
			res.Updated = append(res.Updated, event)
		case stale >= 0:
			entries[stale] = newEntry(want)
			res.Updated = append(res.Updated, event)
		default:
			entries = append(entries, newEntry(want))
			res.Added = append(res.Added, event)
		}
		hooks[event] = entries
	}

	hooksRaw, err := json.Marshal(hooks)
	if err != nil {
		return nil, res, err
	}
	root["hooks"] = hooksRaw
	out, err := marshalPreservingOrder(config, root)
	if err != nil {
		return nil, res, err
	}
	return out, res, nil
}

type hookEntry struct {
	Matcher string    `json:"matcher"`
	Hooks   []hookCmd `json:"hooks"`
}

type hookCmd struct {
	Type    string `json:"type"`
	Command string `json:"command"`
	Timeout int    `json:"timeout,omitempty"`
}

func newEntry(cmd string) json.RawMessage {
	b, _ := json.Marshal(hookEntry{Matcher: "", Hooks: []hookCmd{{Type: "command", Command: cmd, Timeout: HookTimeoutSeconds}}})
	return b
}

// dmuxHookCommand reports whether an entry is one dmux installed (a single
// command hook ending in " hook-event" with an empty matcher) and returns
// its command. Entries with several hooks or other matchers are someone
// else's and are never considered ours.
func dmuxHookCommand(raw json.RawMessage) (string, bool) {
	var e hookEntry
	if json.Unmarshal(raw, &e) != nil || e.Matcher != "" || len(e.Hooks) != 1 {
		return "", false
	}
	h := e.Hooks[0]
	if h.Type != "command" || !strings.HasSuffix(h.Command, " hook-event") {
		return "", false
	}
	return h.Command, true
}

// marshalPreservingOrder re-emits top-level keys in their original order
// (json.Marshal on a map sorts them), with "hooks" appended if new.
func marshalPreservingOrder(original []byte, root map[string]json.RawMessage) ([]byte, error) {
	order := keyOrder(original)
	seen := map[string]bool{}
	var b strings.Builder
	b.WriteString("{\n")
	first := true
	write := func(k string) {
		if !first {
			b.WriteString(",\n")
		}
		first = false
		val, _ := json.MarshalIndent(json.RawMessage(root[k]), "  ", "  ")
		fmt.Fprintf(&b, "  %q: %s", k, val)
	}
	for _, k := range order {
		if _, ok := root[k]; ok && !seen[k] {
			write(k)
			seen[k] = true
		}
	}
	if _, ok := root["hooks"]; ok && !seen["hooks"] {
		write("hooks")
	}
	b.WriteString("\n}\n")
	return []byte(b.String()), nil
}

// keyOrder extracts top-level key order from a JSON object without a full
// streaming parser: decode tokens until depth returns to 1.
func keyOrder(data []byte) []string {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	var keys []string
	depth := 0
	expectKey := false
	for {
		tok, err := dec.Token()
		if err != nil {
			return keys
		}
		switch t := tok.(type) {
		case json.Delim:
			switch t {
			case '{', '[':
				depth++
				expectKey = t == '{' && depth == 1
			case '}', ']':
				depth--
				expectKey = depth == 1
			}
		case string:
			if depth == 1 && expectKey {
				keys = append(keys, t)
				expectKey = false
			} else if depth == 1 {
				expectKey = true
			}
		default:
			if depth == 1 {
				expectKey = true
			}
		}
	}
}

// Remove returns config with every dmux hook entry removed (any binary
// path), preserving everything else. Empty event arrays are dropped, and the
// "hooks" key itself is dropped when nothing remains. Removed reports how
// many entries were taken out.
func Remove(config []byte) ([]byte, int, error) {
	root := map[string]json.RawMessage{}
	if len(strings.TrimSpace(string(config))) > 0 {
		if err := json.Unmarshal(config, &root); err != nil {
			return nil, 0, fmt.Errorf("config is not a JSON object: %w", err)
		}
	}
	raw, ok := root["hooks"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return config, 0, nil
	}
	hooks := map[string][]json.RawMessage{}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, 0, fmt.Errorf(`"hooks" is not an object of event -> array: %w`, err)
	}
	removed := 0
	for event, entries := range hooks {
		kept := entries[:0]
		for _, e := range entries {
			if _, isDmux := dmuxHookCommand(e); isDmux {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, event)
		} else {
			hooks[event] = kept
		}
	}
	if removed == 0 {
		return config, 0, nil
	}
	if len(hooks) == 0 {
		delete(root, "hooks")
	} else {
		hooksRaw, err := json.Marshal(hooks)
		if err != nil {
			return nil, 0, err
		}
		root["hooks"] = hooksRaw
	}
	out, err := marshalPreservingOrder(config, root)
	return out, removed, err
}

// Uninstall removes dmux hooks from the config at path (backup + atomic
// replace). Returns how many entries were removed and the backup path.
func Uninstall(path string) (int, string, error) {
	original, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", nil
	}
	if err != nil {
		return 0, "", err
	}
	out, removed, err := Remove(original)
	if err != nil || removed == 0 {
		return removed, "", err
	}
	backup, err := writeWithBackup(path, original, out)
	return removed, backup, err
}

// Install merges dmux hooks into the config at path, writing a timestamped
// backup first and replacing the file atomically. Returns the result and
// the backup path ("" if nothing changed).
func Install(path, dmuxBin string) (MergeResult, string, error) {
	original, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return MergeResult{}, "", err
	}
	merged, res, err := Merge(original, dmuxBin)
	if err != nil {
		return res, "", err
	}
	if !res.Changed() {
		return res, "", nil
	}
	backup, err := writeWithBackup(path, original, merged)
	return res, backup, err
}

// writeWithBackup writes a timestamped backup of original (if any) next to
// path, then replaces path atomically with content at mode 0600.
func writeWithBackup(path string, original, content []byte) (string, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	backup := ""
	if len(original) > 0 {
		backup = path + ".bak-" + time.Now().UTC().Format("20060102-150405")
		if err := os.WriteFile(backup, original, 0o600); err != nil {
			return "", fmt.Errorf("write backup: %w", err)
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config.json.*.tmp")
	if err != nil {
		return backup, err
	}
	tmpName := tmp.Name()
	cleanup := func(err error) (string, error) { _ = tmp.Close(); _ = os.Remove(tmpName); return backup, err }
	if _, err := tmp.Write(content); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		return cleanup(err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return cleanup(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return cleanup(err)
	}
	return backup, nil
}
