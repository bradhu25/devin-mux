package core

import "path/filepath"

// samePath compares two paths after cleaning and resolving symlinks where
// possible (macOS reports /private/tmp for /tmp; git prints the resolved
// form while dmux records what it was given).
func samePath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	if ea == nil && eb == nil {
		return filepath.Clean(ra) == filepath.Clean(rb)
	}
	return filepath.Clean(a) == filepath.Clean(b)
}
