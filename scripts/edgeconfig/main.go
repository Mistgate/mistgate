// Command edgeconfig writes the wrangler configs of the six edge scripts, rendered from release.NewEdgeConfig.
//
//	go run ./scripts/edgeconfig -out edge/worker          the committed wrangler.<role>.example.toml (go generate ./internal/release)
//	go run ./scripts/edgeconfig -out edge/worker -dev     wrangler.<role>.dev.toml with a zero D1 id, for `wrangler dev --local`
//	go run ./scripts/edgeconfig -list                     the file names, one per line, FRONT (the primary of a dev run) first
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/mistgate/mistgate/internal/release"
)

func main() {
	out := flag.String("out", ".", "directory to write to (edge/worker)")
	dev := flag.Bool("dev", false, "write the .dev.toml copies, with the all-zero D1 id")
	list := flag.Bool("list", false, "print the file names instead of writing them")
	flag.Parse()

	cfg := release.NewEdgeConfig("v0.0.0-dev")
	// FRONT first: in a multi-config `wrangler dev` the first config is the one that listens.
	roles := append([]string{release.RoleFront}, release.EdgeRoles[:len(release.EdgeRoles)-1]...)
	for _, role := range roles {
		name, body := release.WranglerExampleName(role), cfg.WranglerConfig(role)
		if *dev {
			name, body = release.WranglerDevName(role), strings.ReplaceAll(body, release.WranglerD1Placeholder, "00000000-0000-0000-0000-000000000000")
		}
		if *list {
			fmt.Println(name)
			continue
		}
		if err := os.WriteFile(filepath.Join(*out, name), []byte(body), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}
