package connectiondiag

import (
	"fmt"
	"testing"

	"github.com/mistgate/mistgate/internal/plugin/plugintest"
)

// The variant of a VLESS profile, on the inputs the packages that read the settings are tested with (plugintest.Corpus).
func TestVLESSVariantOnTheSharedInputs(t *testing.T) {
	var lines []string
	for _, c := range plugintest.Corpus() {
		lines = append(lines, fmt.Sprintf("%-28s VLESSVariant=%+v", c.ID, VLESSVariant(c.Settings)))
	}
	plugintest.Golden(t, "variant.golden", lines)
}
