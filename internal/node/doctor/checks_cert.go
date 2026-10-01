package doctor

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// cert_expiry: the certificate expires soon or does not match the SNI.
//
// For every enabled inbound with TLS the certificate actually served is read from the inbound's TCP listener
// (Inbound.TLSPort, hysteria2's masquerade port), so an ACME renewal is seen as it happens; the apply-time
// expiry the engine reported is used only for self-signed certificates (it never changes there). The agent's
// own mTLS certificate is checked too. Fix: restart_inbound for a self-signed inbound (a restart regenerates a
// stale self-signed certificate); ACME renews by itself, so there is no fix for it.
func checkCertExpiry(ctx context.Context, e *Env) Result {
	type finding struct {
		id, mode, name string
		st             Status
		left           time.Duration
		reason         string
	}
	now := e.now()
	var fs []finding
	var unknown []string
	checked := 0
	for _, in := range e.Inbounds() {
		if !in.Enabled || in.TLSMode == "" || in.State == "failed" {
			continue // a failed inbound serves nothing; the port and engine checks say why
		}
		var notAfter time.Time
		f := finding{id: in.ID, mode: in.TLSMode, name: in.ServerName, st: OK}
		if in.TLSPort > 0 && in.TLSDown == "" && tcpPortIsOurs(e, in.TLSPort) {
			cctx, cancel := context.WithTimeout(ctx, certDialTime)
			leaf, err := e.ServedCert(cctx, fmt.Sprintf("127.0.0.1:%d", in.TLSPort), in.ServerName)
			cancel()
			if err == nil {
				notAfter = leaf.NotAfter
				if in.ServerName != "" && leaf.VerifyHostname(in.ServerName) != nil {
					f.st, f.reason = Fail, "san_mismatch"
				}
			}
		}
		if notAfter.IsZero() && in.TLSMode == "self_signed" {
			notAfter = in.CertNotAfter
		}
		if notAfter.IsZero() {
			unknown = append(unknown, in.ID)
			continue
		}
		checked++
		f.left = notAfter.Sub(now)
		switch {
		case f.left < certFail:
			f.st = Fail
			if f.reason == "" {
				f.reason = "expiring"
				if f.left < 0 {
					f.reason = "expired"
				}
			}
		case f.left < certWarn && f.st == OK:
			f.st, f.reason = Warn, "expiring"
		}
		fs = append(fs, f)
	}

	// The agent's own certificate.
	agent := finding{id: "agent", st: OK}
	if na := e.AgentCertNotAfter(); !na.IsZero() {
		checked++
		agent.left = na.Sub(now)
		switch {
		case agent.left < agentCertFail:
			agent.st, agent.reason = Fail, "expiring"
		case agent.left < agentCertWarn:
			agent.st, agent.reason = Warn, "expiring"
		}
		if agent.st != OK {
			fs = append(fs, agent)
		}
	}

	if checked == 0 && len(fs) == 0 {
		if len(unknown) > 0 {
			return skip(CodeCertUnreadable, "certificate of "+csv(unknown)+" cannot be read", "inbounds", csv(unknown))
		}
		return skip(CodeCertNone, "no TLS inbound")
	}

	// Worst first; among equals the smaller time left; a self-signed inbound is preferred so the fix fits.
	var rep *finding
	for i := range fs {
		f := &fs[i]
		switch {
		case f.st == OK:
		case rep == nil, f.st > rep.st:
			rep = f
		case f.st == rep.st && f.mode == "self_signed" && rep.mode != "self_signed":
			rep = f
		case f.st == rep.st && f.mode == rep.mode && f.left < rep.left:
			rep = f
		}
	}
	if rep == nil {
		return Result{Status: OK, Code: CodeCertOK, Params: p("checked", strconv.Itoa(checked)), Detail: fmt.Sprintf("%d certificate(s) checked", checked)}
	}
	r := Result{Status: rep.st, Params: p("reason", rep.reason, "days_left", strconv.Itoa(int(rep.left.Hours()/24)))}
	if rep.id == "agent" {
		r.Code = CodeCertAgent
		r.Params["subject"] = "agent"
		r.Detail = fmt.Sprintf("agent certificate %s (%s left)", rep.reason, rep.left.Round(time.Minute))
		return r
	}
	r.Code = CodeCertInbound
	r.Params["inbound_id"] = rep.id
	r.Params["server_name"] = rep.name
	r.Detail = fmt.Sprintf("inbound %s certificate for %s: %s (%s left)", rep.id, rep.name, rep.reason, rep.left.Round(time.Minute))
	if rep.mode == "self_signed" {
		r.FixID = FixRestartInbound
	}
	return r
}
