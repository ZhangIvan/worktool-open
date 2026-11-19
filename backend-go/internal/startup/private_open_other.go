//go:build !linux

package startup

import (
	"errors"
	"os"
)

// This standalone candidate is Linux-only until another platform receives an
// equivalent reviewed safe-open implementation. Never fall back to unsafe open.
func openPrivate(string) (*os.File, error) { return nil, errors.New("STARTUP_PLATFORM_UNSUPPORTED") }
