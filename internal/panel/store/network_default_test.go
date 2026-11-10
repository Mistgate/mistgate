//go:build !js

package store

import (
	"context"
	"testing"
)

// Nothing about people's networks is kept until the owner chooses a mode, and a choice equal to the default is stored
// like any other, so it survives a later change of the default.
func TestNetworkCollectionIsOffUntilChosen(t *testing.T) {
	ctx := context.Background()
	st := openTemp(t)
	mode, err := st.NetworkMode(ctx)
	if err != nil || mode != 0 || DefaultNetworkMode != 0 {
		t.Fatalf("mode of a new installation = %d (default %d), err %v; want 0", mode, DefaultNetworkMode, err)
	}
	for _, chosen := range []uint32{1, 0, 2} {
		if err := st.SaveNetworkMode(ctx, chosen); err != nil {
			t.Fatal(err)
		}
		values, err := st.SettingValues(ctx, []string{SettingNetworkMode})
		if err != nil || values[SettingNetworkMode] == "" {
			t.Fatalf("mode %d was not stored: %q, %v", chosen, values[SettingNetworkMode], err)
		}
		if got, _ := st.NetworkMode(ctx); got != chosen {
			t.Fatalf("stored mode = %d, want %d", got, chosen)
		}
	}
}
