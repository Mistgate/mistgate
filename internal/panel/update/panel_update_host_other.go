//go:build !linux

package update

func panelUpdateHostSupported() bool         { return false }
func canRunPanelUpdateHelper() bool          { return false }
func panelUpdateUsesRootHelperService() bool { return false }
