// Command mistgate-node is the node agent: `enroll` with a panel, `install` the systemd unit, `run` the
// agent, `version`. See internal/node/agent for the protocol.
package main

import (
	"fmt"
	"os"

	"github.com/mistgate/mistgate/internal/buildinfo"
)

const usage = `usage: mistgate-node <command> [flags]

commands:
  enroll    exchange a one-time token for a node certificate (--panel, --sni, --ca-sha256, --token;
            the panel shows this command when you add a node)
  install   write a hardened systemd unit, enable and start it (root, Linux; -h for --state-dir, --bin, --no-start)
  run       run the agent in the foreground: what the systemd unit does, and the way to try it without systemd
  cleanup-net  remove the tunnel interfaces, the WARP routes and the nft tables of the agent (what the unit runs after every stop)
  awg       awg prepare-kernel: install the AmneziaWG kernel module (root, explicit; the default userspace backend needs none)
  version   print the version

Both enroll and run take --state-dir (default /var/lib/mistgate-node); give them the same one.
`

// exitNotEnrolled is EX_CONFIG; the unit lists it in RestartPreventExitStatus so a node that was never
// enrolled (or was retired) does not restart-loop.
const exitNotEnrolled = 78

func main() { os.Exit(dispatch(os.Args[1:])) }

func dispatch(args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "version", "--version":
		fmt.Println("mistgate-node", buildinfo.Version)
		fmt.Println("built", buildinfo.BuiltUnix())
		if key, err := buildinfo.ReleasePublicKey(); err == nil {
			fmt.Println("release key", buildinfo.KeyFingerprint(key))
		} else {
			fmt.Println("release key none (unsigned build: update by hand)")
		}
		return 0
	case "enroll":
		return cmdEnroll(args[1:])
	case "install":
		return cmdInstall(args[1:])
	case "cleanup-net":
		return cmdCleanupNet(args[1:])
	case "awg":
		return cmdAwg(args[1:])
	case "run":
		return cmdRun(args[1:])
	case "help", "-h", "--help":
		fmt.Print(usage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "mistgate-node: unknown command %q\n\n%s", args[0], usage)
	return 2
}

func envOr(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}
