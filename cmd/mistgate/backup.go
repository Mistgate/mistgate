package main

import (
	"io"

	panelbackup "github.com/mistgate/mistgate/internal/panel/backup"
)

func runBackup(args []string, out io.Writer) error {
	return panelbackup.RunCLI(args, out)
}
