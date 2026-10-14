//go:build js && wasm

package bridge

import "github.com/mistgate/mistgate/internal/panel/app"

// The tests run the role with every operation, as cmd/mistgate-edge does: the whole panel, creating the instance, with the SSH
// client of the Provisioner. (Run sets the role in a real wasm; a test has no Run.)
func init() {
	active = Role{Name: "all", Build: app.Build, Auth: true, CreatesInstance: true}
	provisioner = sshProvisioner
}
