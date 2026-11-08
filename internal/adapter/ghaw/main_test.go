package ghaw_test

import (
	"os"
	"testing"

	"github.com/Costroid/costroid/internal/agenttest"
)

// TestMain runs the tests with a stub first on PATH for every agent, so no
// test can start the machine's own.
func TestMain(m *testing.M) {
	os.Exit(agenttest.Main(m.Run))
}
