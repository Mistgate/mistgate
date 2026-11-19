package connectiondiag

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// What the copy of the protocol facts that lives in connectiondiag answers today, on the inputs all the copies are asked about.
func TestFactsCopiesInConnectionDiag(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		lines = append(lines, fmt.Sprintf("%-28s VLESSVariant=%+v", c.ID, VLESSVariant(c.Settings)))
	}
	plugintest.Golden(t, "facts_copies_connectiondiag.golden", lines)
}
