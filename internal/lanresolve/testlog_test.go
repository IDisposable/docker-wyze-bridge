package lanresolve

import (
	"os"
	"testing"

	"github.com/rs/zerolog"
)

// testLogger writes debug-level logs to stderr when -v is given and
// discards them otherwise, so a failing test can be re-run with the
// resolver's reasoning visible.
func testLogger() zerolog.Logger {
	if testing.Verbose() {
		return zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).Level(zerolog.DebugLevel)
	}
	return zerolog.Nop()
}
