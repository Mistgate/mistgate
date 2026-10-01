package agent

import (
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/node/engine"
)

// An ACME certificate does not exist yet when the engine answers Apply, so the ApplyResult carries none. The stats stream
// and every later result must say what the inbound serves once it is there (the node page read "certificate —" for ever).
func TestLateCertificateReachesThePanel(t *testing.T) {
	h := newHarness(t, harnessOpts{})
	h.eng.lateCert = true
	h.waitConnected()
	h.panel.push(fullState(1, inb("inb_1", 0, 0, cred("crd_a"))))
	if r := h.panel.nextApply(); r.Inbounds[0].CertPinSha256 != "" || r.Inbounds[0].CertNotAfterUnix != 0 {
		t.Fatalf("no certificate exists at apply time, the result says %+v", r.Inbounds[0])
	}

	h.eng.mu.Lock()
	h.eng.leaf = engine.CertInfo{PinSHA256: "bb" + "inb_1", NotAfter: time.Unix(2100000000, 0)}
	h.eng.mu.Unlock()
	eventually(t, func() bool {
		st := h.panel.statsSeen()
		if st == nil {
			return false
		}
		for _, ih := range st.Health {
			if ih.InboundId == "inb_1" && ih.CertPinSha256 == "bbinb_1" && ih.CertNotAfterUnix == 2100000000 {
				return true
			}
		}
		return false
	}, "certificate in the stats health")

	// a delivery of what the agent already runs is answered from its cache: it must not blank the certificate again
	h.panel.push(fullState(2, inb("inb_1", 0, 0, cred("crd_a"))))
	r := h.panel.nextApply()
	ir := r.Inbounds[0]
	if r.Revision != 2 || ir.CertPinSha256 != "bbinb_1" || ir.CertNotAfterUnix != 2100000000 {
		t.Fatalf("re-delivered result: %+v", ir)
	}
}
