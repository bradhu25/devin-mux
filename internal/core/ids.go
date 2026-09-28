package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

// Workspace and session ids are short (6 hex chars) because users type
// them and a machine has few of them; callers that create records must
// check for collisions against existing state (see NewUniqueID). Event ids
// are generated thousands of times per session and are never typed, so
// they get 16 hex chars.
const (
	shortIDBytes = 3
	eventIDBytes = 8
)

func newID(prefix string, n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// NewWorkspaceID returns a fresh workspace id, e.g. ws_a91c3f.
func NewWorkspaceID() string { return newID("ws", shortIDBytes) }

// NewSessionID returns a fresh session id, e.g. s_7f2e9a.
func NewSessionID() string { return newID("s", shortIDBytes) }

// NewEventID returns a fresh event id, e.g. e_04b1d8f3a2c47e91.
func NewEventID() string { return newID("e", eventIDBytes) }

// NewUniqueID calls gen until it produces an id for which taken returns
// false. With 6 hex chars and tens of records, a retry is vanishingly rare;
// the loop bound only guards against a broken predicate.
func NewUniqueID(gen func() string, taken func(string) bool) string {
	for i := 0; i < 64; i++ {
		if id := gen(); !taken(id) {
			return id
		}
	}
	panic("NewUniqueID: could not find a free id")
}

// Names are used as directory names, tmux session names, and CLI arguments,
// so they are deliberately restrictive: no spaces, slashes, or shell
// metacharacters. Repo names additionally become subdirectories of Root.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// ValidateName reports whether s is an acceptable workspace or repo name.
func ValidateName(s string) error {
	if !nameRe.MatchString(s) || s == "." || s == ".." {
		return fmt.Errorf("invalid name %q: use letters, digits, '.', '_' or '-', starting with a letter or digit (max 64 chars)", s)
	}
	return nil
}
