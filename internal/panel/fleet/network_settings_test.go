package fleet

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const testNetworkSHA = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

type networkSettingsUpdates struct {
	*fakeUpdates
	onChanged func()
}

func (u *networkSettingsUpdates) SetNetworkBundleChanged(callback func()) { u.onChanged = callback }

func (u *networkSettingsUpdates) changeNetworkTable(name, sha string) {
	u.setNetworkTable(name, sha)
	if u.onChanged != nil {
		u.onChanged()
	}
}

func TestNetworkSettingsAndSignatureKeepOldAgentsStable(t *testing.T) {
	f := &Fleet{}
	f.networkMode.Store(1)
	updates := &fakeUpdates{}
	updates.setNetworkTable("mistgate-asndb-data-any", testNetworkSHA)
	f.SetUpdates(updates)
	node := store.NodeRow{DialTimeoutS: 30, CountryCode: "DE", AwgBackend: "userspace"}
	old := f.nodeSettings(node, nil)
	if got, want := settingsSig(old), "10 20 45 30 []DE"; got != want {
		t.Fatalf("old-agent signature = %q, want the pre-network value %q", got, want)
	}
	if old.Networks != 0 || old.NetworksDbName != "" || old.NetworksDbSha256 != "" {
		t.Fatalf("old agent settings contain network fields: %+v", old)
	}
	capable := f.nodeSettings(node, []string{capNetworks})
	if capable.Networks != 1 || capable.NetworksDbName != "mistgate-asndb-data-any" || capable.NetworksDbSha256 != testNetworkSHA {
		t.Fatalf("nets/1 settings = %+v", capable)
	}
	oldSignature, capableSignature := settingsSig(old), settingsSig(capable)
	f.SetNetworkMode(0)
	off := f.nodeSettings(node, []string{capNetworks})
	if off.Networks != 0 || off.NetworksDbName != "" || off.NetworksDbSha256 != "" || settingsSig(off) != oldSignature {
		t.Fatalf("mode-0 nets/1 settings differ from the pre-feature settings: %+v, signature %q want %q", off, settingsSig(off), oldSignature)
	}
	f.SetNetworkMode(1)
	f.SetNetworkMode(2)
	if settingsSig(f.nodeSettings(node, []string{capNetworks})) == capableSignature {
		t.Fatal("mode change did not change the nets/1 settings signature")
	}
	if settingsSig(f.nodeSettings(node, nil)) != oldSignature {
		t.Fatal("mode change altered the old-agent settings signature")
	}
	updates.setNetworkTable("mistgate-asndb-data-any", strings.Repeat("a", 64))
	if settingsSig(f.nodeSettings(node, []string{capNetworks})) == capableSignature {
		t.Fatal("bundle SHA change did not change the nets/1 settings signature")
	}
	updates.setNetworkTable("", "")
	withoutTable := f.nodeSettings(node, []string{capNetworks})
	if withoutTable.Networks != 2 || withoutTable.NetworksDbName != "" || withoutTable.NetworksDbSha256 != "" {
		t.Fatalf("settings without a table = %+v", withoutTable)
	}
}

func TestNetworkModeReadFailureStartsDisabled(t *testing.T) {
	e := newCoreEnv(t)
	e.exec(`DROP TABLE setting`)
	var output bytes.Buffer
	cfg := e.f.cfg
	cfg.Log = slog.New(slog.NewTextHandler(&output, nil))
	f, err := New(e.st, e.v, e.f.reg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.NetworkMode(); got != 0 {
		t.Fatalf("network mode after settings read failure = %d, want disabled", got)
	}
	if log := output.String(); !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "code=network_setting_read_failed") {
		t.Fatalf("settings read failure was not logged as an error: %s", log)
	}
}

func TestNetworkModeAndBundleChangesResendSettings(t *testing.T) {
	e := newEnv(t)
	e.run()
	a := e.enroll("nodea")
	e.fixture(a.nodeID)
	updates := &networkSettingsUpdates{fakeUpdates: &fakeUpdates{}}
	updates.setNetworkTable("mistgate-asndb-data-any", testNetworkSHA)
	e.f.SetUpdates(updates)
	updates.SetNetworkBundleChanged(e.f.StateChanged)
	c, _, initial := connectCaps(a, "nets-settings", capNetworks)
	if initial.Settings == nil || initial.Settings.Networks != 1 || initial.Settings.NetworksDbSha256 != testNetworkSHA {
		t.Fatalf("initial network settings = %+v", initial.Settings)
	}
	if !e.f.SetNetworkMode(2) {
		t.Fatal("mode 1 -> 2 reported no change")
	}
	e.f.StateChanged()
	modeDelta := c.desired()
	if modeDelta.Settings == nil || modeDelta.Settings.Networks != 2 || len(modeDelta.Inbounds) != 0 {
		t.Fatalf("mode change settings delta = %+v", modeDelta)
	}
	updates.changeNetworkTable("mistgate-asndb-data-any", strings.Repeat("b", 64))
	bundleDelta := c.desired()
	if bundleDelta.Settings == nil || bundleDelta.Settings.NetworksDbSha256 != strings.Repeat("b", 64) || len(bundleDelta.Inbounds) != 0 {
		t.Fatalf("bundle change settings delta = %+v", bundleDelta)
	}
	if !e.f.SetNetworkMode(0) {
		t.Fatal("mode 2 -> 0 reported no change")
	}
	off := e.f.nodeSettings(store.NodeRow{DialTimeoutS: 30, CountryCode: "DE"}, []string{capNetworks})
	preFeature := nodeSettings(store.NodeRow{DialTimeoutS: 30, CountryCode: "DE"}, nil)
	if off.Networks != 0 || off.NetworksDbName != "" || off.NetworksDbSha256 != "" || settingsSig(off) != settingsSig(preFeature) {
		t.Fatalf("mode-0 settings/signature = %+v/%q; want network fields absent and pre-feature signature %q", off, settingsSig(off), settingsSig(preFeature))
	}
}

func TestGuardNetworksCapabilitiesModesCapsAndCounters(t *testing.T) {
	f := &Fleet{}
	f.networkMode.Store(1)
	sha := testNetworkSHA
	batch := &agentv1.StatsBatch{NetworksDbSha256: sha, Networks: make([]*agentv1.NetworkCounters, 0, 520),
		PersonNetworks: []*agentv1.PersonNetwork{{CredId: "crd_alice", InboundId: "inb_de1", Asn: 64496, Connects: 1}}}
	for i := 0; i < 513; i++ {
		batch.Networks = append(batch.Networks, &agentv1.NetworkCounters{InboundId: fmt.Sprintf("inb_%03d", i), Asn: 64496 + uint32(i%16),
			Attempts: ^uint32(0), Connects: 2})
	}
	batch.Networks = append(batch.Networks,
		&agentv1.NetworkCounters{InboundId: "inb_de1", Attempts: 1},
		&agentv1.NetworkCounters{InboundId: "inb_nl1", Attempts: 1},
		&agentv1.NetworkCounters{InboundId: "inb_fi1", Attempts: 1},
		&agentv1.NetworkCounters{InboundId: "inb_de1", Attempts: 1})
	networks, persons, gotSHA := f.guardNetworks(batch, []string{capNetworks})
	if len(networks) != 515 || len(persons) != 0 || gotSHA != sha {
		t.Fatalf("mode 1 guard kept %d networks, %d people, sha %q; want 512 nonzero + 3 zero, no people", len(networks), len(persons), gotSHA)
	}
	zeroRows := 0
	for _, row := range networks {
		if row.ASN == 0 {
			zeroRows++
		}
		if row.Attempts > store.MaxLinkCount {
			t.Errorf("counter = %d, above clamp %d", row.Attempts, store.MaxLinkCount)
		}
	}
	if zeroRows != 3 {
		t.Fatalf("ASN-0 overflow rows = %d, want one for each inbound", zeroRows)
	}
	if got, _, gotSHA := f.guardNetworks(batch, nil); len(got) != 0 || gotSHA != "" {
		t.Fatalf("agent without nets/1 kept %d rows or digest %q", len(got), gotSHA)
	}
	f.networkMode.Store(0)
	if got, people, gotSHA := f.guardNetworks(batch, []string{capNetworks}); len(got) != 0 || len(people) != 0 || gotSHA != sha {
		t.Fatalf("off guard kept %d rows/%d people, sha %q", len(got), len(people), gotSHA)
	}
	f.networkMode.Store(2)
	batch.PersonNetworks = make([]*agentv1.PersonNetwork, 2050)
	for i := range batch.PersonNetworks {
		batch.PersonNetworks[i] = &agentv1.PersonNetwork{CredId: "crd_alice", InboundId: "inb_de1", Asn: 64496, Connects: ^uint32(0)}
	}
	_, persons, _ = f.guardNetworks(batch, []string{capNetworks})
	if len(persons) != 2048 || persons[0].Connects != store.MaxLinkCount {
		t.Fatalf("mode 2 guard kept %d person rows with first counter %d, want 2048 and %d", len(persons), persons[0].Connects, store.MaxLinkCount)
	}
}
