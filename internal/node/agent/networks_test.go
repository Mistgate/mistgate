package agent

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/asndb"
	"github.com/mistgate/mistgate/internal/node/nets"
	"github.com/mistgate/mistgate/internal/node/netstest"
	"google.golang.org/protobuf/proto"
)

func testNetworkTableBytes(t *testing.T, asn uint32) []byte {
	t.Helper()
	raw, err := netstest.LoopbackBytes(asn)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func networkSettings(raw []byte) *pb.NodeSettings {
	return &pb.NodeSettings{Networks: 1, NetworksDbName: networksBundleName, NetworksDbSha256: fileSHA(raw)}
}

func setNetworkBundle(p *fakePanel, raw []byte) {
	p.mu.Lock()
	if p.bundle == nil {
		p.bundle = make(map[string][]byte)
	}
	p.bundle[networksBundleName] = append([]byte(nil), raw...)
	p.mu.Unlock()
}

func networkFetchCount(p *fakePanel) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.fetches)
}

func hasCapability(hello *pb.Hello, cap string) bool {
	for _, value := range hello.GetCapabilities() {
		if value == cap {
			return true
		}
	}
	return false
}

func TestNetworkTableFetchCacheReloadAndHello(t *testing.T) {
	firstRaw := testNetworkTableBytes(t, 64500)
	firstSHA := fileSHA(firstRaw)
	h := newHarness(t, harnessOpts{noStart: true})
	setNetworkBundle(h.panel, firstRaw)
	h.panel.settings.Store(networkSettings(firstRaw))
	h.start()
	h.waitConnected()
	firstHello := <-h.panel.hellos
	if !hasCapability(firstHello, capNets) || firstHello.GetNetworksDbSha256() != "" {
		t.Fatalf("initial Hello capabilities=%v table_sha=%q", firstHello.GetCapabilities(), firstHello.GetNetworksDbSha256())
	}
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == firstSHA }, "network table fetch")
	if n := networkFetchCount(h.panel); n != 1 {
		t.Fatalf("FetchUpdate requests = %d, want one", n)
	}
	if got := h.a.nets.Lookup(netip.MustParseAddr("127.0.0.1")); got != 64500 {
		t.Fatalf("loaded network table ASN = %d, want 64500", got)
	}

	h.panel.push(&pb.DesiredState{Revision: 1, Settings: networkSettings(firstRaw)})
	if applied := h.panel.nextApply(); applied.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("persist network settings: %+v", applied)
	}
	if err := h.stop(); err != nil {
		t.Fatal(err)
	}
	h.a = h.newAgent()
	h.start()
	h.waitConnected()
	streamsAfterRestart := h.panel.connCount()
	secondHello := <-h.panel.hellos
	if !hasCapability(secondHello, capNets) || (secondHello.GetNetworksDbSha256() != "" && secondHello.GetNetworksDbSha256() != firstSHA) {
		t.Fatalf("restart Hello capabilities=%v table_sha=%q", secondHello.GetCapabilities(), secondHello.GetNetworksDbSha256())
	}
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == firstSHA }, "network table cache load after Hello")
	eventually(t, func() bool {
		stats := h.panel.statsSeen()
		return stats != nil && stats.GetNetworksDbSha256() == firstSHA
	}, "StatsBatch to report the loaded table without reconnecting")
	if n := h.panel.connCount(); n != streamsAfterRestart {
		t.Fatalf("loaded table digest required a reconnect: streams before=%d after=%d", streamsAfterRestart, n)
	}
	if n := networkFetchCount(h.panel); n != 1 {
		t.Fatalf("restart fetched a cached network table: requests=%d", n)
	}

	secondRaw := testNetworkTableBytes(t, 64501)
	secondSHA := fileSHA(secondRaw)
	setNetworkBundle(h.panel, secondRaw)
	secondSettings := networkSettings(secondRaw)
	h.panel.settings.Store(secondSettings)
	h.panel.push(&pb.DesiredState{Revision: 2, BaseRevision: 1, Settings: secondSettings})
	if applied := h.panel.nextApply(); applied.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("apply changed network table settings: %+v", applied)
	}
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == secondSHA }, "network table reload")
	if got := h.a.nets.Lookup(netip.MustParseAddr("127.0.0.1")); got != 64501 {
		t.Fatalf("reloaded network table ASN = %d, want 64501", got)
	}
	if n := networkFetchCount(h.panel); n != 2 {
		t.Fatalf("changed table FetchUpdate requests = %d, want two total", n)
	}
}

func TestNetworkTableDigestMismatchRetriesAndJournals(t *testing.T) {
	wanted := testNetworkTableBytes(t, 64500)
	served := testNetworkTableBytes(t, 64501)
	h := newHarness(t, harnessOpts{noStart: true})
	setNetworkBundle(h.panel, served)
	h.panel.settings.Store(networkSettings(wanted))
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return networkFetchCount(h.panel) >= 2 }, "network table retry after digest mismatch")
	if got := h.a.nets.LoadedSHA(); got != "" {
		t.Fatalf("mismatched table became active with digest %q", got)
	}
	eventually(t, func() bool {
		stats := h.panel.statsSeen()
		if stats == nil {
			return false
		}
		for _, entry := range stats.Journal {
			if entry.Attrs["code"] == "networks_table_unavailable" && strings.Contains(entry.Message, "network table unavailable") {
				return true
			}
		}
		return false
	}, "network table warning journal entry")
}

func TestConfigureNetworksDoesNotReadCachedFile(t *testing.T) {
	raw := testNetworkTableBytes(t, 64500)
	h := newHarness(t, harnessOpts{noStart: true})
	if err := os.WriteFile(filepath.Join(h.dir, networksFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h.a.configureNetworks(networkSettings(raw))
	if got := h.a.nets.LoadedSHA(); got != "" {
		t.Fatalf("configureNetworks loaded the cached table synchronously: %q", got)
	}
}

func TestNetworkTableModeChangeDoesNotWakeOrResetBackoff(t *testing.T) {
	wanted := testNetworkTableBytes(t, 64500)
	served := testNetworkTableBytes(t, 64501)
	h := newHarness(t, harnessOpts{noStart: true})
	h.a.backoffMin, h.a.backoffMax = 500*time.Millisecond, 500*time.Millisecond
	setNetworkBundle(h.panel, served)
	h.panel.settings.Store(networkSettings(wanted))
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return networkFetchCount(h.panel) == 1 }, "first network table fetch")

	settings := networkSettings(wanted)
	settings.Networks = 2
	h.panel.push(&pb.DesiredState{Revision: 1, Settings: settings})
	if result := h.panel.nextApply(); result.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("change network mode with same target: %+v", result)
	}
	h.a.networkWake <- struct{}{} // A same-target wake must not bypass the in-flight backoff timer.
	time.Sleep(100 * time.Millisecond)
	if n := networkFetchCount(h.panel); n != 1 {
		t.Fatalf("same-target wake reset network fetch backoff: fetches=%d", n)
	}
	eventually(t, func() bool { return networkFetchCount(h.panel) >= 2 }, "network table retry after backoff")
}

func TestNetworkTableBusyRetryIsQuiet(t *testing.T) {
	raw := testNetworkTableBytes(t, 64500)
	logs := &netstest.LogBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(cfg *Config) {
		cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	}})
	setNetworkBundle(h.panel, raw)
	h.panel.mu.Lock()
	h.panel.fetchBusy = 1
	h.panel.mu.Unlock()
	h.panel.settings.Store(networkSettings(raw))
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == fileSHA(raw) }, "network table fetch after a busy response")
	if n := networkFetchCount(h.panel); n != 2 {
		t.Fatalf("busy retry fetch count = %d, want two", n)
	}
	if strings.Contains(logs.String(), "networks_table_unavailable") {
		t.Fatalf("busy response produced an unavailable log/journal record: %s", logs.String())
	}
}

func TestPermanentNetworkFetchFailureWaitsForTargetChange(t *testing.T) {
	first := testNetworkTableBytes(t, 64500)
	second := testNetworkTableBytes(t, 64501)
	logs := &netstest.LogBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(cfg *Config) {
		cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	}})
	h.panel.settings.Store(networkSettings(first)) // Deliberately no bundle: FetchUpdate returns ErrPermanent.
	h.start()
	h.waitConnected()
	eventually(t, func() bool {
		return journalNetworkUnavailable(h.panel.statsSeen())
	}, "permanent network table failure journal")
	if n := networkFetchCount(h.panel); n != 1 {
		t.Fatalf("permanent fetch failure retried: fetch count=%d", n)
	}
	if count := strings.Count(logs.String(), "networks_table_unavailable"); count != 1 {
		t.Fatalf("permanent fetch failure log count=%d, want one", count)
	}
	setNetworkBundle(h.panel, second)
	settings := networkSettings(second)
	h.panel.push(&pb.DesiredState{Revision: 1, Settings: settings})
	if result := h.panel.nextApply(); result.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("change target after permanent failure: %+v", result)
	}
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == fileSHA(second) }, "table fetch after target change")
	if n := networkFetchCount(h.panel); n != 2 {
		t.Fatalf("changed target fetch count=%d, want two", n)
	}
}

func TestInvalidNetworkBundleNameStopsWithoutFetching(t *testing.T) {
	raw := testNetworkTableBytes(t, 64500)
	settings := networkSettings(raw)
	settings.NetworksDbName = "mistgate-asndb-invalid"
	logs := &netstest.LogBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(cfg *Config) {
		cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	}})
	h.panel.settings.Store(settings)
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return journalNetworkUnavailable(h.panel.statsSeen()) }, "invalid bundle name journal")
	time.Sleep(100 * time.Millisecond)
	if n := networkFetchCount(h.panel); n != 0 {
		t.Fatalf("invalid bundle name fetched %d times", n)
	}
	if count := strings.Count(logs.String(), "networks_table_unavailable"); count != 1 {
		t.Fatalf("invalid bundle name log count=%d, want one", count)
	}
}

func TestMatchingMalformedCachedTableIsPermanentAndKeepsWorkingTable(t *testing.T) {
	malformed := []byte("invalid asn table fixture")
	good := testNetworkTableBytes(t, 64501)
	logs := &netstest.LogBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(cfg *Config) {
		cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	}})
	oldDB := testDBForAgent(t)
	oldSHA := fileSHA(testNetworkTableBytes(t, 64500))
	h.a.nets.SetMode(1)
	h.a.nets.SetTable(oldDB, oldSHA)
	if err := os.WriteFile(filepath.Join(h.dir, networksFileName), malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	h.panel.settings.Store(networkSettings(malformed))
	h.start()
	h.waitConnected()
	eventually(t, func() bool { return journalNetworkUnavailable(h.panel.statsSeen()) }, "malformed matching cache journal")
	if got := h.a.nets.LoadedSHA(); got != oldSHA {
		t.Fatalf("malformed target displaced the working table: loaded SHA=%q", got)
	}
	if got := h.a.nets.Lookup(netip.MustParseAddr("127.0.0.1")); got != 64500 {
		t.Fatalf("working table lookup changed during invalid update: ASN=%d", got)
	}
	time.Sleep(100 * time.Millisecond)
	if n := networkFetchCount(h.panel); n != 0 {
		t.Fatalf("matching malformed cache was fetched/retried %d times", n)
	}
	if count := strings.Count(logs.String(), "networks_table_unavailable"); count != 1 {
		t.Fatalf("matching malformed cache log count=%d, want one", count)
	}
	setNetworkBundle(h.panel, good)
	settings := networkSettings(good)
	h.panel.push(&pb.DesiredState{Revision: 1, Settings: settings})
	if result := h.panel.nextApply(); result.Status != pb.ApplyStatus_APPLY_STATUS_APPLIED {
		t.Fatalf("apply a new valid table target: %+v", result)
	}
	eventually(t, func() bool { return h.a.nets.LoadedSHA() == fileSHA(good) }, "new valid table after permanent cache failure")
}

func journalNetworkUnavailable(batch *pb.StatsBatch) bool {
	if batch == nil {
		return false
	}
	for _, entry := range batch.GetJournal() {
		if entry.GetAttrs()["code"] == "networks_table_unavailable" {
			return true
		}
	}
	return false
}

func TestNetworkCountersAreTakenOnlyAcrossUTCWindowBoundary(t *testing.T) {
	h := newHarness(t, harnessOpts{noStart: true})
	h.a.nets.SetMode(1)
	h.a.nets.SetTable(testDBForAgent(t), "fixture-sha")
	h.a.nets.Add("inb_de1", 64500, nets.Attempt, "", "")
	start := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	before := h.a.buildBatch(context.Background(), start, start.Add(9*time.Minute))
	if len(before.Networks) != 0 {
		t.Fatalf("network rows left before a 10-minute boundary: %+v", before.Networks)
	}
	boundary := h.a.buildBatch(context.Background(), start.Add(9*time.Minute), start.Add(10*time.Minute))
	if len(boundary.Networks) != 1 || boundary.Networks[0].InboundId != "inb_de1" || boundary.Networks[0].Asn != 64500 || boundary.Networks[0].Attempts != 1 {
		t.Fatalf("boundary network rows = %+v", boundary.Networks)
	}
	if next := h.a.buildBatch(context.Background(), start.Add(10*time.Minute), start.Add(11*time.Minute)); len(next.Networks) != 0 {
		t.Fatalf("Take retained a completed window: %+v", next.Networks)
	}
}

func TestAddressNeverLeavesBatchHelloOrAgentLogs(t *testing.T) {
	logs := &netstest.LogBuffer{}
	h := newHarness(t, harnessOpts{noStart: true, cfg: func(cfg *Config) {
		cfg.Log = slog.New(slog.NewTextHandler(logs, nil))
	}})
	raw := testNetworkTableBytes(t, 64500)
	db := testDBForAgent(t)
	h.a.nets.SetMode(1)
	h.a.nets.SetTable(db, fileSHA(raw))
	addr := netip.MustParseAddr("127.0.0.2")
	h.a.nets.Add("inb_de1", h.a.nets.Lookup(addr), nets.Connect, "", "")
	h.a.log.Warn("fixture warning") // Exercise both captured agent logs and the journal field in buildBatch.
	start := time.Date(2026, 11, 10, 10, 0, 0, 0, time.UTC)
	batch := h.a.buildBatch(context.Background(), start, start.Add(10*time.Minute))
	if len(batch.GetJournal()) == 0 {
		t.Fatal("buildBatch omitted the fixture journal entry")
	}
	if batch.GetNetworksDbSha256() != fileSHA(raw) {
		t.Fatalf("StatsBatch loaded table digest=%q", batch.GetNetworksDbSha256())
	}
	hello := h.a.hello(context.Background()).GetHello()
	if hello.GetNetworksDbSha256() != fileSHA(raw) {
		t.Fatalf("Hello loaded table digest=%q", hello.GetNetworksDbSha256())
	}
	batchWire, err := proto.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	helloWire, err := proto.Marshal(hello)
	if err != nil {
		t.Fatal(err)
	}
	forms, err := netstest.AddressForms(addr.String())
	if err != nil {
		t.Fatal(err)
	}
	logText := []byte(logs.String())
	for _, form := range forms {
		if bytes.Contains(batchWire, form) || bytes.Contains(helloWire, form) || bytes.Contains(logText, form) {
			t.Fatal("agent output contains a client address form")
		}
	}
}

func testDBForAgent(t *testing.T) *asndb.DB {
	t.Helper()
	raw := testNetworkTableBytes(t, 64500)
	db, err := asndb.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	return db
}
