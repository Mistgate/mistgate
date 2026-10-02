//go:build !linux

package update

func panelUpdateHostSupported() bool { return false }
func canRunPanelUpdateHelper() bool  { return false }
func panelUpdateRuntimeArch() string { return "unsupported" }
