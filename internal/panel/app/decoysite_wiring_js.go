//go:build js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
)

// The library of decoy sites is the VPS edition's: the Workers keep no bundles and none of its code (proto/mistgate/decoysite/v1),
// only this stub, which names the service by its path so no generated code is linked, and refuses every procedure the way the
// admin words a feature the edition lacks.
const decoySiteServicePath = "/mistgate.decoysite.v1.DecoySiteService/"

func (k *core) addDecoySite(cfg *httpserver.Config) {
	refuseInEdition(cfg, decoySiteServicePath, edition.DecoyOwn)
}
