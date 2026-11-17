package mtproto

import (
	"io"
	"net/http"
	"testing"

	"github.com/mistgate/mistgate/internal/node/engine"
	"github.com/mistgate/mistgate/internal/node/sitehttp/sitehttptest"
)

// The decoy site answers HTTP/2 with the SETTINGS and the connection window of every other site of the node.
func TestDecoySpeaksHTTP2LikeEverySite(t *testing.T) {
	certs := &fakeCerts{acme: testCert(t, "acme."+ownDomain), self: testCert(t, ownDomain)}
	env := engine.Env{Certs: certs, Masquerade: func(string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "decoy") })
	}}
	f := newFixture(t, env, `{"domains":["de1.example.com"],"fallback":"decoy"}`, credential("c1", key(1)))
	sitehttptest.Check(t, "MTProto decoy", f.dial())
}
