package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"

	opsv1 "github.com/mistgate/mistgate/gen/mistgate/ops/v1"
	"github.com/mistgate/mistgate/gen/mistgate/ops/v1/opsv1connect"
	"github.com/mistgate/mistgate/internal/panel/ops"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/store/storetest"
	"github.com/mistgate/mistgate/internal/panel/update"
)

type networkSettingsNotifierProbe struct {
	store          *store.Store
	mode           uint32
	stateChanges   int
	storedAtChange uint32
}

func (p *networkSettingsNotifierProbe) SetNetworkMode(mode uint32) bool {
	changed := p.mode != mode
	p.mode = mode
	return changed
}

func (p *networkSettingsNotifierProbe) StateChanged() {
	p.stateChanges++
	mode, _ := p.store.NetworkMode(context.Background())
	p.storedAtChange = mode
}

type networkBundleUpdaterProbe struct{ callback func() }

func (p *networkBundleUpdaterProbe) SetNetworkBundleChanged(callback func()) { p.callback = callback }

type networkBundleFleetProbe struct{ stateChanges int }

func (p *networkBundleFleetProbe) StateChanged() { p.stateChanges++ }

func TestNetworkSettingsAndBundleCallbacksAreWired(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, storetest.Path(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	updater, err := update.New(st, nil, nil, update.Config{StepUp: func(context.Context) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	notifier := &networkSettingsNotifierProbe{store: st, mode: 1}
	service := ops.New(ops.Config{
		Store:                  st,
		StepUp:                 func(context.Context) error { return nil },
		NetworkTable:           func() ops.NetworkTable { return networkTableForOps(updater) },
		NetworkBundle:          updater.NetworkTableFile,
		NetworkSettingsChanged: networkSettingsChanged(notifier),
	})
	path, handler := service.Handler()
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client := opsv1connect.NewOpsServiceClient(server.Client(), server.URL)

	changed, err := client.SetNetworkSettings(ctx, connect.NewRequest(&opsv1.SetNetworkSettingsRequest{Mode: 2}))
	if err != nil {
		t.Fatal(err)
	}
	if notifier.mode != 2 || notifier.stateChanges != 1 || notifier.storedAtChange != 1 {
		t.Fatalf("mode callback state = mode %d, changes %d, persisted mode during callback %d; want 2/1/1",
			notifier.mode, notifier.stateChanges, notifier.storedAtChange)
	}
	if changed.Msg.Settings.Mode != 2 || changed.Msg.Settings.TableBuiltUnix != 0 {
		t.Fatalf("SetNetworkSettings response = %+v; want mode 2 and no loaded table", changed.Msg.Settings)
	}
	read, err := client.GetNetworkSettings(ctx, connect.NewRequest(&opsv1.GetNetworkSettingsRequest{}))
	if err != nil || read.Msg.TableBuiltUnix != 0 {
		t.Fatalf("production no-table GetNetworkSettings = %+v, %v; want built time 0", read, err)
	}

	fleetProbe := &networkBundleFleetProbe{}
	updaterProbe := &networkBundleUpdaterProbe{}
	wireNetworkBundleChanged(fleetProbe, updaterProbe)
	if updaterProbe.callback == nil {
		t.Fatal("network bundle callback was not registered")
	}
	updaterProbe.callback()
	if fleetProbe.stateChanges != 1 {
		t.Fatalf("bundle callback caused %d fleet state changes, want 1", fleetProbe.stateChanges)
	}
}
