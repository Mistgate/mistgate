package agent

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/plugin"
)

// testDoctorEnv points the doctor at an empty fake root with no commands and canned answers, so agent tests
// never read the machine they run on and behave the same on Windows and Linux.
func testDoctorEnv(t *testing.T) func(doctor.Env) doctor.Env {
	root := t.TempDir()
	no := errors.New("not in tests")
	return func(e doctor.Env) doctor.Env {
		e.Root, e.Unsupported = root, ""
		e.Run = func(context.Context, string, ...string) ([]byte, error) { return nil, no }
		e.LookPath = func(string) (string, error) { return "", no }
		e.Statfs = func(string) (doctor.FSStat, error) {
			return doctor.FSStat{BlockSize: 1 << 20, Blocks: 100000, Bfree: 50000, Bavail: 50000, Files: 1000, Ffree: 900}, nil
		}
		e.Dial = func(context.Context, string, string) error { return no }
		e.Lookup = func(context.Context, string) error { return nil }
		e.LookupVia = func(context.Context, string, string) error { return nil }
		e.ServedCert = func(context.Context, string, string) (*x509.Certificate, error) { return nil, no }
		e.Docker = func(context.Context) ([]doctor.Container, error) { return nil, no }
		e.Readlink = func(string) (string, error) { return "", no }
		return e
	}
}

func nextDoctor(t *testing.T, h *harness) *pb.DoctorReport {
	t.Helper()
	select {
	case r := <-h.panel.doctors:
		return r
	case <-time.After(8 * time.Second):
		t.Fatal("no DoctorReport")
		return nil
	}
}

func resultIDs(r *pb.DoctorReport) string {
	var ids []string
	for _, x := range r.Results {
		ids = append(ids, x.Id)
	}
	return strings.Join(ids, ",")
}

func TestHelloAdvertisesTheDoctor(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	hello := <-h.panel.hellos
	found := false
	for _, c := range hello.Capabilities {
		found = found || c == "doctor/1"
	}
	if !found {
		t.Fatalf("Hello.capabilities = %v", hello.Capabilities)
	}
}

func TestPeriodicDoctorReportIsUnsolicitedAndUnreliable(t *testing.T) {
	h := newHarness(t, harnessOpts{doctorFirst: 30 * time.Millisecond, doctorEvery: 60 * time.Millisecond})
	for i := 0; i < 2; i++ {
		r := nextDoctor(t, h)
		if r.RequestId != "" || r.Partial || r.Error != "" {
			t.Fatalf("report %d: %+v", i, r)
		}
		if got := resultIDs(r); got != strings.Join(doctor.CheckIDs(), ",") {
			t.Fatalf("a full report lists every check in order, got %s", got)
		}
		for _, x := range r.Results {
			if x.TitleKey != "doctor."+x.Id+".title" || x.MeasuredUnix == 0 || x.Status == pb.DoctorStatus_DOCTOR_STATUS_UNSPECIFIED {
				t.Errorf("bad result %+v", x)
			}
		}
	}
	if n := h.panel.badSeq.Load(); n != 0 {
		t.Errorf("%d DoctorReports carried a seq: they must not go through the reliable outbox", n)
	}
}

func TestNoReportBeforeTheFirstDelay(t *testing.T) {
	h := newHarness(t, harnessOpts{}) // default: 30 s after connect
	h.waitConnected()
	select {
	case r := <-h.panel.doctors:
		t.Fatalf("unsolicited report right after connect: %+v", r)
	case <-time.After(300 * time.Millisecond):
	}
}

func TestStatusValuesMatchTheProto(t *testing.T) {
	for want, got := range map[pb.DoctorStatus]doctor.Status{
		pb.DoctorStatus_DOCTOR_STATUS_OK: doctor.OK, pb.DoctorStatus_DOCTOR_STATUS_WARN: doctor.Warn,
		pb.DoctorStatus_DOCTOR_STATUS_FAIL: doctor.Fail, pb.DoctorStatus_DOCTOR_STATUS_SKIP: doctor.Skip,
	} {
		if pb.DoctorStatus(got) != want {
			t.Errorf("doctor.%s = %d, proto %s = %d", got, got, want, want)
		}
	}
}

func TestRunDoctorEchoesTheRequest(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RunDoctor{RunDoctor: &pb.RunDoctor{RequestId: "req_d1"}}})
	r := nextDoctor(t, h)
	if r.RequestId != "req_d1" || r.Partial || len(r.Results) != len(doctor.CheckIDs()) {
		t.Fatalf("%+v", r)
	}
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RunDoctor{RunDoctor: &pb.RunDoctor{RequestId: "req_d2", Checks: []string{"disk_space", "nope"}}}})
	r = nextDoctor(t, h)
	if r.RequestId != "req_d2" || !r.Partial || resultIDs(r) != "disk_space,nope" {
		t.Fatalf("%+v", r)
	}
	if u := r.Results[1]; u.Status != pb.DoctorStatus_DOCTOR_STATUS_SKIP || u.Detail != "unknown check" {
		t.Errorf("unknown id: %+v", u)
	}
	if r.Results[0].Status != pb.DoctorStatus_DOCTOR_STATUS_OK { // 50% used in the canned statfs
		t.Errorf("disk_space: %+v", r.Results[0])
	}
}

// A RunDoctor while a run is on gets DoctorReport{error: busy}; the stream keeps working meanwhile.
func TestRunDoctorWhileBusy(t *testing.T) {
	in, release := make(chan struct{}), make(chan struct{})
	base := testDoctorEnv(t)
	opened := false
	h := newHarness(t, harnessOpts{doctorEnv: func(e doctor.Env) doctor.Env {
		e = base(e)
		e.Statfs = func(string) (doctor.FSStat, error) {
			if !opened {
				opened = true
				close(in)
			}
			<-release
			return doctor.FSStat{BlockSize: 1 << 20, Blocks: 100, Bfree: 50, Bavail: 50}, nil
		}
		return e
	}})
	h.waitConnected()
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RunDoctor{RunDoctor: &pb.RunDoctor{RequestId: "req_a", Checks: []string{"disk_space"}}}})
	<-in
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_RunDoctor{RunDoctor: &pb.RunDoctor{RequestId: "req_b"}}})
	r := nextDoctor(t, h)
	if r.RequestId != "req_b" || r.Error != "busy" || len(r.Results) != 0 {
		t.Fatalf("second run: %+v", r)
	}
	// The stream is not blocked by the run that is still going: a Ping is answered.
	h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_Ping{Ping: &pb.Ping{Nonce: 7}}})
	close(release)
	if r := nextDoctor(t, h); r.RequestId != "req_a" || len(r.Results) != 1 {
		t.Fatalf("first run: %+v", r)
	}
}

func TestApplyFixUnknownAndBadParams(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	for _, tc := range []struct {
		fix    string
		params map[string]string
		want   string
	}{
		{"rm -rf /", nil, "unknown_fix"},
		{"enable_ntp", nil, "unknown_fix"},
		{"restart_inbound", map[string]string{"inbound_id": "inb_none"}, "bad_params"},
		{"journald_vacuum", map[string]string{"x": "y"}, "bad_params"},
		{"journald_vacuum", nil, "unsupported_host"}, // the fake host has no Fixer
	} {
		h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_ApplyFix{ApplyFix: &pb.ApplyFix{RequestId: "req_f", FixId: tc.fix, Params: tc.params}}})
		r := h.panel.nextCmd()
		if r.RequestId != "req_f" || r.Ok || r.Error != tc.want {
			t.Errorf("%s %v: %+v", tc.fix, tc.params, r)
		}
	}
}

// restart_inbound is the one fix that goes through the agent's worker; dry run first, then the real thing,
// then the partial re-check report.
func TestApplyFixRestartInbound(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("c1"))))
	h.panel.nextApply()
	applies := h.eng.applyCount("inb_1")

	fix := func(dry bool) *pb.CommandResult {
		h.panel.send(&pb.ConnectResponse{Message: &pb.ConnectResponse_ApplyFix{ApplyFix: &pb.ApplyFix{
			RequestId: "req_fix", FixId: "restart_inbound", DryRun: dry, Params: map[string]string{"inbound_id": "inb_1"},
		}}})
		return h.panel.nextCmd()
	}
	r := fix(true)
	if !r.Ok || r.RequestId != "req_fix" || r.Params["inbounds"] != "inb_1" || r.Params["disruptive"] != "1" || r.Detail == "" {
		t.Fatalf("dry run: %+v", r)
	}
	if h.eng.applyCount("inb_1") != applies {
		t.Fatal("a dry run restarted the inbound")
	}
	select {
	case rep := <-h.panel.doctors:
		t.Fatalf("a dry run sent a report: %+v", rep)
	case <-time.After(200 * time.Millisecond):
	}

	r = fix(false)
	if !r.Ok || r.Affected != 1 || r.Error != "" {
		t.Fatalf("real run: %+v", r)
	}
	eventually(t, func() bool { return h.eng.applyCount("inb_1") == applies+1 }, "inbound re-applied by the worker")
	rep := nextDoctor(t, h)
	if rep.RequestId != "" || !rep.Partial || resultIDs(rep) != "port_conflicts,cert_expiry" {
		t.Fatalf("re-check report: %+v", rep)
	}
}

func TestSamplesFeedTheRing(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	eventually(t, func() bool { return len(h.a.hostRing.Since(h.a.now(), time.Hour)) >= 3 }, "host samples in the ring")
	s := h.a.hostRing.Since(h.a.now(), time.Hour)
	if s[0].CPU != 12.5 || s[0].Softirq != 3 || s[0].Load1 != 0.5 { // fakeHost.Metrics
		t.Errorf("sample = %+v", s[0])
	}
}

func TestDoctorInboundsDescribeTheAppliedState(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 20000, 20100, cred("c1"))))
	h.panel.nextApply()
	got := h.a.doctorInbounds()
	if len(got) != 1 {
		t.Fatalf("%+v", got)
	}
	in := got[0]
	if in.ID != "inb_1" || in.Network != "udp" || in.Port != 443 || in.HopFrom != 20000 || in.HopTo != 20100 || !in.Enabled ||
		in.State != "running" || in.TLSMode != "self_signed" || in.ServerName != "example.com" || in.Egress != "direct" {
		t.Errorf("%+v", in)
	}
	if in.CertNotAfter.Unix() != 2000000000 { // what the fake engine reported at apply time
		t.Errorf("CertNotAfter = %v", in.CertNotAfter)
	}
	if in.TLSPort != 0 { // only hysteria2 has a readable TLS listener
		t.Errorf("TLSPort = %d for a fake protocol", in.TLSPort)
	}
}

func TestHy2TCPPort(t *testing.T) {
	mk := func(proto, settings string, tls plugin.TLSMode) plugin.InboundSpec {
		return plugin.InboundSpec{Protocol: proto, Settings: json.RawMessage(settings), TLS: plugin.TLS{Mode: tls}}
	}
	for _, tc := range []struct {
		spec plugin.InboundSpec
		want int
	}{
		{mk("hysteria2", `{}`, plugin.TLSSelfSigned), 443}, // absent = 443
		{mk("hysteria2", ``, plugin.TLSSelfSigned), 443},   // no settings at all
		{mk("hysteria2", `{"masquerade":{"tcp_port":8443}}`, plugin.TLSAcmeDomain), 8443},
		{mk("hysteria2", `{"masquerade":{"tcp_port":0}}`, plugin.TLSSelfSigned), 0}, // 0 = no HTTPS listener
		{mk("hysteria2", `{"masquerade":{"tcp_port":-1}}`, plugin.TLSSelfSigned), 0},
		{mk("hysteria2", `not json`, plugin.TLSSelfSigned), 0},
		{mk("awg", `{}`, plugin.TLSSelfSigned), 0},
		{mk("hysteria2", `{}`, 0), 0},
	} {
		if got := hy2TCPPort(tc.spec); got != tc.want {
			t.Errorf("%s %s mode %d: %d, want %d", tc.spec.Protocol, tc.spec.Settings, tc.spec.TLS.Mode, got, tc.want)
		}
	}
}
