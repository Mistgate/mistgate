//go:build js && wasm

package main

import (
	"context"
	"errors"
	"net/http"
	"syscall/js"
	"time"

	"github.com/mistgate/mistgate/edge/d1driver"
	"github.com/mistgate/mistgate/internal/buildinfo"
	"github.com/mistgate/mistgate/internal/panel/provision"
	"github.com/mistgate/mistgate/internal/panel/update"
)

// The edge runs SSH installs in a second Worker script whose one Durable Object (the Provisioner) holds the sockets (design
// §4): the same panel.wasm runs there in the "serve" role, with sshConnect, and the main Worker runs it in the "forward"
// role, where the ProvisioningService and the install page are only a marker (X-Mistgate-Forward) for the Worker to send the
// request on. Init option, both roles:
//
//	provision: { role: "forward" | "serve", kick?: () => Promise<void>, sshConnect?: (host, port, timeoutMs) => Promise<socket> }
//
// kick wakes the Provisioner (sets its alarm); serve needs it and sshConnect, forward takes no sshConnect. Without the
// option a panel is a forwarder with no kick.

var (
	errInvalidProvision   = errors.New(`provision must be {role: "forward" | "serve", kick?: function, sshConnect?: function}; a serve role needs both functions and a forward role no sshConnect`)
	errNotProvisioner     = errors.New("mgPanel.provision runs only in the serve role of the Provisioner Worker")
	errProvisionerNoPanel = errors.New("the Provisioner serves an initialised panel: deploy and open the main Worker first")
)

const (
	provisionPassBudget = 14 * time.Minute // a Durable Object alarm may run 15
	buildHeader         = "X-Mistgate-Build"
	panelUpdatingBody   = `{"code":"unavailable","message":"panel_updating"}`
)

type provisionOptions struct {
	serve      bool
	kick       js.Value // undefined when the option has none
	sshConnect js.Value
}

func parseProvisionOptions(options js.Value) (provisionOptions, error) {
	var out provisionOptions
	if !optionPresent(options, "provision") {
		return out, nil
	}
	value := options.Get("provision")
	if value.Type() == js.TypeUndefined || value.IsNull() {
		return out, nil
	}
	if value.Type() != js.TypeObject {
		return out, errInvalidProvision
	}
	role, err := stringOption(value, "role")
	if err != nil {
		return out, errInvalidProvision
	}
	switch role {
	case "", "forward":
	case "serve":
		out.serve = true
	default:
		return out, errInvalidProvision
	}
	for _, name := range []string{"kick", "sshConnect"} {
		if v := value.Get(name); v.Type() != js.TypeUndefined && !v.IsNull() && v.Type() != js.TypeFunction {
			return out, errInvalidProvision
		}
	}
	if kick := value.Get("kick"); kick.Type() == js.TypeFunction {
		out.kick = kick
	}
	if connect := value.Get("sshConnect"); connect.Type() == js.TypeFunction {
		out.sshConnect = connect
	}
	hasConnect := out.sshConnect.Type() == js.TypeFunction
	if out.serve != hasConnect || out.serve && out.kick.Type() != js.TypeFunction {
		return out, errInvalidProvision
	}
	return out, nil
}

// wake calls the Worker's kick and waits for it; the cron pass and a queued install use it to start the Provisioner's alarm.
func (o provisionOptions) wake(ctx context.Context) error {
	promise, err := safeInvoke(o.kick)
	if err != nil {
		return err
	}
	if promise.Type() != js.TypeObject || promise.Get("then").Type() != js.TypeFunction {
		return nil
	}
	_, err = d1driver.Await(ctx, promise)
	return err
}

// releaseNodeBinaries is where the Provisioner gets the agent it uploads: this panel's own release, verified with the
// compiled-in key. A development build has neither tag nor key and gets agent_bundle_unavailable at the upload step.
func releaseNodeBinaries() *update.ReleaseNodeBinaries {
	key, _ := buildinfo.ReleasePublicKey()
	return &update.ReleaseNodeBinaries{Version: buildinfo.Version, Key: key}
}

// newSSHClient is the SSH client of the Provisioner: its sockets from sshConnect, names through DNS-over-HTTPS.
func newSSHClient(sshConnect js.Value) *provision.Client {
	return provision.NewClientWith(sshSockets{connect: sshConnect}.DialContext, provision.DoHLookup(nil))
}

// requireBuild answers 503 panel_updating to a request that does not name this build in X-Mistgate-Build: the main Worker
// stamps it from the marker, so two scripts of different builds (a deploy in between) never run one request between them.
func requireBuild(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(buildHeader) != buildinfo.Version {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(panelUpdatingBody))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// provisionPass is mgPanel.provision(): one Drain pass for the Provisioner's alarm. It resolves {more, busy, waitUntil}:
// more, another install waits; busy, a pass is already running in this isolate and nothing was done.
func provisionPass() (js.Value, error) {
	stateMu.RLock()
	current := state
	stateMu.RUnlock()
	if current == nil {
		return js.Undefined(), errNotInitialized
	}
	if !current.serve {
		return js.Undefined(), errNotProvisioner
	}
	defer current.afterResponse.release()
	ctx, cancel := context.WithTimeout(context.Background(), provisionPassBudget)
	defer cancel()
	more, busy, err := current.panel.Provision.Drain(ctx)
	if err != nil {
		return js.Undefined(), err
	}
	out := js.Global().Get("Object").New()
	out.Set("more", more)
	out.Set("busy", busy)
	out.Set("waitUntil", current.afterResponse.WaitUntil())
	return out, nil
}
