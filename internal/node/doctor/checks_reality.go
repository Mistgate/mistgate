package doctor

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"
)

// TargetFacts is what a handshake with a REALITY target showed. Err is "" when TLS 1.3 completed, "unreachable" when
// the target did not answer in time or refused, "no_tls13" when it answered but not with TLS 1.3. The rest is only
// meaningful when Err is "".
type TargetFacts struct {
	Err   string
	MLKEM bool // the handshake chose X25519MLKEM768
	// Cert is "" when the leaf is good for the name, "name_mismatch" (the leaf is not for this name), "untrusted" (the
	// chain does not lead to a system root) or "expired" (reported by the chain check).
	Cert     string
	NotAfter time.Time // of the leaf
}

// reality_target: is the site a REALITY inbound shows to strangers still a good one?
//
// The engine probes the target once, when the inbound starts. This check repeats it with every doctor run (10 minutes) for
// each enabled VLESS REALITY inbound that shows another site (not "self"): the target must answer, speak TLS 1.3 and choose
// X25519MLKEM768 (a client that offers it, which new clients do, gets a broken handshake from a target that cannot), and
// its certificate must be good for the name, lead to a system root and have more than 7 days left. A failed handshake is
// retried once after a second so one lost packet is not an alert. There is no fix: changing the target is the owner's
// decision (the profile's settings).
func checkRealityTarget(ctx context.Context, e *Env) Result {
	type target struct{ addr, name, key string }
	type finding struct {
		inbound string
		t       target
		st      Status
		hint    string
		days    int
	}
	var ts []target
	owners := map[string][]string{} // target key -> inbound ids
	for _, in := range e.Inbounds() {
		if !in.Enabled || in.RealityTarget == "" || in.RealityName == "" || in.State == "failed" {
			continue
		}
		t := target{addr: in.RealityTarget, name: in.RealityName}
		t.key = strings.ToLower(t.addr + "|" + t.name)
		if _, ok := owners[t.key]; !ok {
			if len(ts) == maxRealityTargets {
				continue
			}
			ts = append(ts, t)
		}
		owners[t.key] = append(owners[t.key], in.ID)
	}
	if len(ts) == 0 {
		return skip(CodeRealityNone, "no REALITY inbound shows another site")
	}

	facts := make([]TargetFacts, len(ts))
	var wg sync.WaitGroup
	for i, t := range ts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			facts[i] = probeWithRetry(ctx, e, t.addr, t.name)
		}()
	}
	wg.Wait()

	now := e.now()
	var fs []finding
	bad := map[string]bool{}
	for i, t := range ts {
		st, hint, days := classifyTarget(facts[i], now)
		if st == OK {
			continue
		}
		for _, id := range owners[t.key] {
			bad[id] = true
		}
		fs = append(fs, finding{inbound: owners[t.key][0], t: t, st: st, hint: hint, days: days})
	}
	if len(fs) == 0 {
		return Result{Status: OK, Code: CodeRealityOK, Params: p("checked", strconv.Itoa(len(ts))), Detail: fmt.Sprintf("%d REALITY target(s) answer with TLS 1.3 and X25519MLKEM768", len(ts))}
	}
	// FAIL before WARN; among equals the first inbound.
	rep := fs[0]
	for _, f := range fs[1:] {
		if f.st > rep.st || f.st == rep.st && f.inbound < rep.inbound {
			rep = f
		}
	}
	ids := make([]string, 0, len(bad))
	for id := range bad {
		ids = append(ids, id)
	}
	r := Result{Status: rep.st, Code: "reality_target." + rep.hint, Params: p(
		"hint", rep.hint, "inbound_id", rep.inbound, "inbounds", csv(ids), "target", rep.t.addr, "server_name", rep.t.name,
		"checked", strconv.Itoa(len(ts)))}
	switch rep.hint {
	case "expiring":
		r.Params["days_left"] = strconv.Itoa(rep.days)
	case "expired":
		r.Params["days_left"] = "0"
	}
	r.Detail = fmt.Sprintf("REALITY target %s for %s (inbound %s): %s", rep.t.addr, rep.t.name, rep.inbound, strings.ReplaceAll(rep.hint, "_", " "))
	return r
}

// classifyTarget turns the facts into a status and a hint (the "hint" param and the tail of the detail code); days is the
// whole days a certificate has left when it is "expiring".
func classifyTarget(f TargetFacts, now time.Time) (st Status, hint string, days int) {
	switch {
	case f.Err == "unreachable":
		return Fail, "unreachable", 0
	case f.Err != "":
		return Fail, "no_tls13", 0
	case !f.MLKEM:
		return Fail, "no_mlkem", 0
	case f.Cert == "name_mismatch":
		return Warn, "name_mismatch", 0
	case f.Cert == "untrusted":
		return Warn, "untrusted", 0
	case f.Cert == "expired" || !f.NotAfter.IsZero() && !f.NotAfter.After(now):
		return Warn, "expired", 0
	}
	if left := f.NotAfter.Sub(now); !f.NotAfter.IsZero() && left < realityCertWarn {
		return Warn, "expiring", int(left / (24 * time.Hour))
	}
	return OK, "", 0
}

// probeWithRetry handshakes once and, when the target looks broken, once more after a second: a lost packet or a
// busy neighbour at the wrong moment is not worth an alert. Worst case 2 x realityAttempt + realityRetryGap, inside the
// check's 10 s budget.
func probeWithRetry(ctx context.Context, e *Env, addr, name string) TargetFacts {
	try := func() TargetFacts {
		actx, cancel := context.WithTimeout(ctx, realityAttempt)
		defer cancel()
		if e.ProbeTarget != nil {
			return e.ProbeTarget(actx, addr, name)
		}
		return probeTLSTarget(actx, addr, name, e.TargetRoots, e.now())
	}
	f := try()
	if f.Err == "" && f.MLKEM || ctx.Err() != nil {
		return f
	}
	e.Sleep(ctx, realityRetryGap)
	if ctx.Err() != nil {
		return f
	}
	return try()
}

// probeTLSTarget is the production ProbeTarget: a TLS 1.3 handshake that offers X25519MLKEM768 and X25519 with the name as
// SNI, then the leaf checked against the name and the roots (nil means the system roots; with none at all the chain is not
// judged, only the name and the dates). The certificate is only looked at, nothing is sent over the connection.
func probeTLSTarget(ctx context.Context, addr, name string, roots *x509.CertPool, now time.Time) TargetFacts {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return TargetFacts{Err: "unreachable"}
	}
	defer conn.Close()
	c := tls.Client(conn, &tls.Config{
		ServerName: name, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13,
		CurvePreferences:   []tls.CurveID{tls.X25519MLKEM768, tls.X25519},
		InsecureSkipVerify: true, // #nosec G402 -- the chain is checked below, to tell the cases apart
	})
	if err := c.HandshakeContext(ctx); err != nil {
		if ctx.Err() != nil {
			return TargetFacts{Err: "unreachable"}
		}
		return TargetFacts{Err: "no_tls13"}
	}
	cs := c.ConnectionState()
	f := TargetFacts{MLKEM: cs.CurveID == tls.X25519MLKEM768}
	if len(cs.PeerCertificates) == 0 {
		f.Cert = "untrusted"
		return f
	}
	leaf := cs.PeerCertificates[0]
	f.NotAfter = leaf.NotAfter
	if leaf.VerifyHostname(name) != nil {
		f.Cert = "name_mismatch"
		return f
	}
	if roots == nil {
		if sys, err := x509.SystemCertPool(); err == nil && !sys.Equal(x509.NewCertPool()) {
			roots = sys
		}
	}
	if roots == nil {
		return f
	}
	inter := x509.NewCertPool()
	for _, ic := range cs.PeerCertificates[1:] {
		inter.AddCert(ic)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		var inv x509.CertificateInvalidError
		if errors.As(err, &inv) && inv.Reason == x509.Expired {
			f.Cert = "expired"
		} else {
			f.Cert = "untrusted"
		}
	}
	return f
}
