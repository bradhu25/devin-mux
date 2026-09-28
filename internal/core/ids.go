package core

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
)

const idBytes = 3 // 6 hex chars: enough for a local tool, short enough to type

func newID(prefix string) string {
	b := make([]byte, idBytes)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	return prefix + "_" + hex.EncodeToString(b)
}

// NewWorkspaceID returns a fresh stable workspace id, e.g. ws_a91c3f.
func NewWorkspaceID() string { return newID("ws") }

// NewSessionID returns a fresh stable session id, e.g. s_7f2e9a.
func NewSessionID() string { return newID("s") }

// NewEventID returns a fresh event id, e.g. e_04b1d8.
func NewEventID() string { return newID("e") }

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
