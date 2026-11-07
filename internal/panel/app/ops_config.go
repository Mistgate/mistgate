package app

import (
	"log/slog"

	"github.com/mistgate/mistgate/internal/logring"
)

// OpsConfig is what the VPS process hands the panel for its own diagnostics: the log ring and the adjustable level of the logger
// it built (logring.New), and what it started with. A nil Ring (tests, the edge) leaves "panel logs" empty.
type OpsConfig struct {
	Ring         *logring.Ring
	Level        *slog.LevelVar
	DefaultLevel slog.Level
	Format       string // "text" or "json"
}
