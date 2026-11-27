//go:build js

package app

import (
	"github.com/mistgate/mistgate/internal/panel/edition"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	nodeprovision "github.com/mistgate/mistgate/internal/panel/provision"
)

// The Cloudflare edition has no subscription host: it has a public address of its own, and its Workers keep no connection open.
// The service is named by its path, so no generated code is linked, and every procedure is refused the way the admin words a
// feature the edition lacks.
const subHostServicePath = "/mistgate.subhost.v1.SubscriptionHostService/"

func newSubHost(*core) subHostService { return nil }

func (k *core) subHostProvision(*nodeprovision.Config) {}

func (k *core) addSubHost(cfg *httpserver.Config) {
	refuseInEdition(cfg, subHostServicePath, edition.SubscriptionHost)
}
