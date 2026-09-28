package state

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestAppendEvent_ConcurrentAppendersNeverInterleave(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())

	const writers, perWriter = 16, 200
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				line, _ := json.Marshal(map[string]int{"w": w, "i": i, "pad": 1 << 20})
				if err := AppendEvent("s_test", line); err != nil {
					t.Error(err)
					return
				}
			}
		}(w)
	}
	wg.Wait()

	dir, _ := EventsDir()
	f, err := os.Open(filepath.Join(dir, "s_test.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })

	n := 0
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var v map[string]int
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			t.Fatalf("line %d is not valid JSON (interleaved write?): %q", n, sc.Text())
		}
		n++
	}
	if n != writers*perWriter {
		t.Fatalf("got %d lines, want %d", n, writers*perWriter)
	}
}

func TestAppendEvent_RejectsUnsafeIDs(t *testing.T) {
	t.Setenv(EnvRoot, t.TempDir())
	for _, id := range []string{"", "../etc", "a/b", "-x", "with space"} {
		if err := AppendEvent(id, []byte(`{}`)); err == nil {
			t.Errorf("expected error for id %q", id)
		}
	}
}
