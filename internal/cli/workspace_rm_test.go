package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestConfirm(t *testing.T) {
	var out bytes.Buffer
	for input, want := range map[string]bool{"y\n": true, "YES\n": true, "n\n": false, "\n": false, "maybe\n": false} {
		ok, err := confirm(strings.NewReader(input), &out, "? ")
		if err != nil || ok != want {
			t.Errorf("confirm(%q) = %v %v, want %v", input, ok, err, want)
		}
	}
	// Immediate EOF (e.g. </dev/null) is "not interactive", not "no".
	if _, err := confirm(strings.NewReader(""), &out, "? "); !errors.Is(err, errNeedYes) {
		t.Fatalf("EOF: want errNeedYes, got %v", err)
	}
	// A pipe is detected before prompting.
	r, w, _ := os.Pipe()
	_ = w.Close()
	defer func() { _ = r.Close() }()
	out.Reset()
	if _, err := confirm(r, &out, "? "); !errors.Is(err, errNeedYes) || out.Len() != 0 {
		t.Fatalf("pipe: want errNeedYes without prompt, got %v %q", err, out.String())
	}
}

func TestDescribeFlags(t *testing.T) {
	if describeFlags(false, false, false) != "" {
		t.Fatal("no flags -> empty")
	}
	if got := describeFlags(true, true, true); !strings.Contains(got, "DISCARD") || !strings.Contains(got, "stop") || !strings.Contains(got, "branches") {
		t.Fatalf("got %q", got)
	}
}
