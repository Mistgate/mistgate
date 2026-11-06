//go:build !js

package update

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/release"
)

func TestSetUpdateChannelStepUpAuditAndCacheInvalidation(t *testing.T) {
	e := newEnv(t)
	stable := newPanelFixture()
	stable.tag, stable.built = "v1.2.0", 1_770_000_000
	testingRelease := newPanelFixture()
	testingRelease.tag, testingRelease.built = "v1.2.1-testing.1", 1_780_000_000
	dev := newPanelFixture()
	dev.tag, dev.built = "v1.2.2-dev.1", 1_790_000_000
	client, _ := panelFixturesClient(t, stable, testingRelease, dev)
	channel := func() release.Channel {
		value, err := e.st.Setting(e.ctx, UpdateChannelSetting)
		if err != nil {
			return release.ChannelStable
		}
		parsed, ok := release.ParseChannel(value)
		if !ok {
			return release.ChannelStable
		}
		return parsed
	}
	updater := NewGitHubPanelUpdater(PanelUpdateConfig{
		CurrentBuilt: dev.built, Key: panelTestPub, DataDir: t.TempDir(), ServiceUnit: "mistgate.service",
		Executable: filepath.Join(t.TempDir(), "mistgate"), Enabled: true, OS: "linux", Arch: "amd64", HTTPClient: client,
		Channel: channel, Now: func() time.Time { return panelTestNow },
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.s.cfg.PanelVersion = "v1.2.2-dev.1"
	e.s.cfg.PanelUpdater = updater
	if status := updater.Check(e.ctx); status.Version != stable.tag {
		t.Fatalf("default stable check = %+v", status)
	}
	if response := e.get(); response.UpdateChannel != "stable" || response.RunningVersionChannel != "dev" || response.OfferedReleaseChannel != "stable" {
		t.Fatalf("stable updates view = %+v", response)
	}
	cachedAt := updater.Status().CheckedUnix

	denied := connect.NewError(connect.CodeFailedPrecondition, errors.New("step-up required"))
	e.stepUp = func(context.Context) error { return denied }
	if _, err := (rpc{e.s}).SetUpdateChannel(e.ctx, connect.NewRequest(&adminv1.SetUpdateChannelRequest{Channel: "testing"})); err != denied {
		t.Fatalf("SetUpdateChannel without step-up = %v, want %v", err, denied)
	}
	if _, err := e.st.Setting(e.ctx, UpdateChannelSetting); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("step-up failure wrote setting: %v", err)
	}
	if rows, err := e.st.ListAudit(e.ctx, "", 0, 10); err != nil || len(rows) != 0 {
		t.Fatalf("step-up failure wrote audit rows %+v, %v", rows, err)
	}
	if updater.Status().CheckedUnix != cachedAt {
		t.Fatal("step-up failure invalidated the cached check")
	}

	e.stepUp = func(context.Context) error { return nil }
	if _, err := (rpc{e.s}).SetUpdateChannel(e.ctx, connect.NewRequest(&adminv1.SetUpdateChannelRequest{Channel: "invalid"})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid channel error = %v", err)
	}
	response, err := (rpc{e.s}).SetUpdateChannel(e.ctx, connect.NewRequest(&adminv1.SetUpdateChannelRequest{Channel: "testing"}))
	if err != nil || response.Msg.Channel != "testing" {
		t.Fatalf("SetUpdateChannel = %+v, %v", response, err)
	}
	if value, err := e.st.Setting(e.ctx, UpdateChannelSetting); err != nil || value != "testing" {
		t.Fatalf("stored channel = %q, %v", value, err)
	}
	if updater.Status().CheckedUnix != 0 {
		t.Fatalf("channel change left cached status valid: %+v", updater.Status())
	}
	status := updater.Check(e.ctx)
	if status.Version != testingRelease.tag || status.Channel != release.ChannelTesting {
		t.Fatalf("next check did not use new channel: %+v", status)
	}
	updates := e.get()
	if updates.UpdateChannel != "testing" || updates.RunningVersionChannel != "dev" || updates.OfferedReleaseChannel != "testing" || !updates.RunningNewerThanChannelOffers {
		t.Fatalf("channel-aware updates view = %+v", updates)
	}

	rows, err := e.st.ListAudit(e.ctx, "", 0, 10)
	if err != nil || len(rows) != 1 || rows[0].Action != "update.channel" {
		t.Fatalf("channel audit rows = %+v, %v", rows, err)
	}
	var params map[string]string
	if err := json.Unmarshal([]byte(rows[0].Params), &params); err != nil || params["old"] != "stable" || params["new"] != "testing" {
		t.Fatalf("channel audit params = %q, %v", rows[0].Params, err)
	}
}

func TestInitialUpdateChannelSeedsOnlyWhenUnset(t *testing.T) {
	e := newEnv(t)
	newWithInitial := func(initial string) *Service {
		e.t.Helper()
		s, err := New(e.st, e.fl, e.hl, Config{
			PanelVersion: "v1.2.3", PanelBuilt: newBuilt, InitialChannel: initial,
			StepUp: func(context.Context) error { return nil },
			Log:    slog.New(slog.NewTextHandler(io.Discard, nil)), Now: e.clk.Now,
		})
		if err != nil {
			e.t.Fatal(err)
		}
		return s
	}
	newWithInitial("dev")
	if value, err := e.st.Setting(e.ctx, UpdateChannelSetting); err != nil || value != "dev" {
		t.Fatalf("first start channel = %q, %v", value, err)
	}
	if err := e.st.SetSettingIfChanged(e.ctx, UpdateChannelSetting, "testing"); err != nil {
		t.Fatal(err)
	}
	newWithInitial("stable")
	if value, err := e.st.Setting(e.ctx, UpdateChannelSetting); err != nil || value != "testing" {
		t.Fatalf("installer environment overwrote owner's channel = %q, %v", value, err)
	}
}
