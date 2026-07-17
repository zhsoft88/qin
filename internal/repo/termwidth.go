package repo

import (
	"fmt"
	"os"
)

// TermWidth returns the terminal width in columns, defaulting to 80.
// Uses platform-specific console API when available, falls back to COLUMNS env.
func TermWidth() int {
	// Try system-specific console API first
	if w := termWidthFromSystem(); w > 0 {
		return w
	}
	// Fallback to COLUMNS env var
	if s := os.Getenv("COLUMNS"); s != "" {
		var w int
		if _, err := fmt.Sscanf(s, "%d", &w); err == nil && w > 0 {
			return w
		}
	}
	return 80
}
