//go:build !js

package mcp

// Why handshakes fail (agent.proto LinkCounters, links/3), as block_diagnosis, access_report and user_diagnose read it. Each
// signal needs its counters to be known for the inbound (the agent lists links/3, the protocol, its transport and the AWG
// backend can see them: accTotals.Unsupported); a counter that is not known never counts as a zero, it is named in unknown.

const (
	minReasonCount = 3  // events of one kind before a reason is read at all
	answerLostPct  = 80 // AWG: the share of responses never confirmed that says the answers do not come back
)

func (t accTotals) known(f string) bool { return !t.unsupportedHas(f) }

// keyReasonFields are the links/3 counters a key of this protocol can carry at all (DeviceConnection): what user_diagnose
// reports as not_known when the row's unsupported list has them.
func keyReasonFields(proto string) []string {
	switch normProtocol(proto) {
	case "awg":
		return []string{"initiations", "responses", "responses_unmatched"}
	case "vless":
		return []string{"tcp_stalls", "tcp_stalls_16k"}
	}
	return nil
}

// oursHellos are the hellos for the inbound's own server name.
func (t accTotals) oursHellos() uint32 { return t.Hellos - min(t.Hellos, t.HellosForeign) }

// answerLost reports that the node answered the handshakes and the answers did not come back: a QUIC handshake that timed out
// after the node's flight (Hysteria2), a REALITY handshake whose client never finished (VLESS), or AmneziaWG responses the
// devices never confirmed. It is what a path that lets the first packet in and drops the reply looks like.
func (t accTotals) answerLost(proto string) (bool, map[string]string) {
	switch normProtocol(proto) {
	case "hysteria2", "vless":
		ours := t.oursHellos()
		if t.known("hs_unfinished") && t.HsUnfinished >= minHellosNoHS && t.HsUnfinished*2 >= ours {
			return true, map[string]string{"hs_unfinished": u32(t.HsUnfinished), "hellos": u32(ours)}
		}
	case "awg":
		if t.known("responses_unmatched") && t.Responses >= minReasonCount && t.ResponsesUnmatched*100 >= t.Responses*answerLostPct {
			return true, map[string]string{"responses": u32(t.Responses), "responses_unmatched": u32(t.ResponsesUnmatched)}
		}
	}
	return false, nil
}

// tcpFreeze reports VLESS connections that stall after the first 8 to 32 KiB, the TSPU's freeze: high when half of the
// connections or more froze that way.
func (t accTotals) tcpFreeze() (bool, string, map[string]string) {
	if !t.known("tcp_stalls_16k") || t.TCPStalls16K < minReasonCount || t.TCPStalls16K*2 < t.TCPStalls {
		return false, "", nil
	}
	conf := confMedium
	if t.Connects > 0 && t.TCPStalls16K*2 >= t.Connects {
		conf = confHigh
	}
	return true, conf, map[string]string{"tcp_stalls": u32(t.TCPStalls), "tcp_stalls_16k": u32(t.TCPStalls16K), "connects": u32(t.Connects)}
}

// reasonsUnknown says which reason counters of the inbound are not known, and why, for the unknown list of a class.
func (t accTotals) reasonsUnknown(proto string) []string {
	switch normProtocol(proto) {
	case "hysteria2":
		if !t.known("hs_unfinished") {
			return []string{"why the handshakes fail is not known: the node's agent does not send the links/3 counters (update the agent)"}
		}
	case "awg":
		if !t.known("responses_unmatched") {
			return []string{"whether the node answers the initiations, and whether its answers arrive, is not known: an agent without links/3, or the kernel AmneziaWG backend, whose statistics cannot tell initiations from responses (the userspace backend can)"}
		}
	case "vless":
		if !t.known("hs_unfinished") && !t.known("tcp_stalls") {
			return []string{"VLESS handshake failures and TCP stalls are not known: an agent without links/3"}
		}
	}
	return nil
}

// addReasonClasses adds what only the reasons tell: our own side refusing (or being refused) and the REALITY handshake cut
// after the hello. The TCP freeze and the lost answers of UDP protocols feed the classes of addCounterClasses.
func (b *blockCtx) addReasonClasses(n *accNode, tcpWorks bool, add func(blockClassV), broken map[uint32]bool, have func(cls, scope string) bool) {
	for _, x := range b.inbounds[n.ID] {
		if !x.Enabled || x.State == "disabled" || x.State == "failed" || broken[x.Port] {
			continue
		}
		t := b.in.Totals[x.ID]
		proto := normProtocol(x.Protocol)
		scope := "inbound:" + x.ID
		ev := []string{"conn:" + x.ID}
		// the app got the node's certificate and refused it: a pin that no longer matches, an expired certificate
		if t.known("tls_cert_refused") && t.TLSCertRefused >= minReasonCount {
			conf := confMedium
			if t.TLSCertRefused >= 10 || t.TLSCertRefused*2 >= t.TLSCertRefused+t.Accepted+t.Connects {
				conf = confHigh
			}
			add(blockClassV{Class: clsBroken, Scope: scope, Confidence: conf, hard: t.Connects == 0, inbound: x.ID, port: x.Port, protocol: proto, Evidence: ev,
				Detail: map[string]string{"reason": "certificate_refused", "tls_cert_refused": u32(t.TLSCertRefused), "tls_alerts_recv": u32(t.TLSAlertsRecv)},
				Explain: "Apps reached the node, got its certificate and refused it (" + u32(t.TLSCertRefused) + " handshakes): a pinned certificate that changed, " +
					"an expired or a self-signed one. A middlebox would have been refused itself, so this is our side: check the certificate and that people refreshed their subscription."})
		}
		// initiations of known keys that the node did not answer
		if proto == "awg" && t.known("responses") && t.Initiations >= 5 && t.Responses*5 < t.Initiations {
			add(blockClassV{Class: clsBroken, Scope: scope, Confidence: confHigh, hard: true, inbound: x.ID, port: x.Port, protocol: proto, Evidence: ev,
				Detail: map[string]string{"reason": "initiations_unanswered", "initiations": u32(t.Initiations), "responses": u32(t.Responses)},
				Explain: "Handshake initiations of known keys reach the node and it answers few of them (" + u32(t.Responses) + " of " + u32(t.Initiations) +
					"): replays, a flood limit or an engine failure on the node. Not a block: look at the node's journal for the inbound."})
		}
		// REALITY: hellos with our name that fail authentication
		if proto == "vless" && t.known("auth_failed") && t.AuthFailed >= 5 && t.AuthFailed >= t.Connects {
			conf, hard := confLow, false
			if t.Connects == 0 {
				conf, hard = confMedium, true
			}
			add(blockClassV{Class: clsBroken, Scope: scope, Confidence: conf, hard: hard, inbound: x.ID, port: x.Port, protocol: proto, Evidence: ev,
				Detail:  map[string]string{"reason": "reality_auth_failed", "auth_failed": u32(t.AuthFailed), "connects": u32(t.Connects)},
				Unknown: []string{"probes fail the same way: if people connect fine, these are probes"},
				Explain: "ClientHellos with this server's name fail REALITY authentication (" + u32(t.AuthFailed) + ", against " + u32(t.Connects) +
					" connections): apps with another public key or short id, as after a key change that subscriptions did not get, or probes."})
		}
		// REALITY answered, the client never finished: cut after the ClientHello
		if proto == "vless" && !have(clsFingerprinted, "protocol:vless") {
			if lost, d := t.answerLost(proto); lost && t.Connects*2 < t.oursHellos() {
				conf := confLow
				if tcpWorks {
					conf = confMedium
				}
				d["connects"] = u32(t.Connects)
				add(blockClassV{Class: clsFingerprinted, Scope: "protocol:vless", Confidence: conf, hard: true, inbound: x.ID, protocol: proto, Evidence: ev, Detail: d,
					Explain: "REALITY answered the ClientHellos with this server's name and the clients never finished the handshake (" + d["hs_unfinished"] + " of " + d["hellos"] +
						"): the path lets the hello in and cuts what follows, as a DPI that recognises the handshake does. XHTTP behind a CDN or a UDP protocol avoids it."})
			}
		}
	}
}

// reasonDetail adds the inbound's reason counters to a class's detail.
func reasonDetail(d map[string]string, extra map[string]string) {
	for k, v := range extra {
		d[k] = v
	}
}
