package app

func wireNetworkBundleChanged(fl interface{ StateChanged() }, upd interface{ SetNetworkBundleChanged(func()) }) {
	if fl != nil && upd != nil {
		upd.SetNetworkBundleChanged(fl.StateChanged)
	}
}
