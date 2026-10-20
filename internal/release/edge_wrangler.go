package release

import (
	"fmt"
	"strings"
)

// The wrangler configs of the six scripts are not written by hand: they are rendered from NewEdgeConfig, the same description
// the bundle carries as edge.json, so that a binding cannot be in one and not in the other. `go generate ./internal/release`
// writes them to edge/worker (scripts/edgeconfig); TestWranglerConfigsAreCurrent fails when the committed files differ.
//
//go:generate go run ../../scripts/edgeconfig -out ../../edge/worker

// WranglerD1Placeholder stands in the rendered configs for the id of the deployer's D1 database.
const WranglerD1Placeholder = "replace-with-your-d1-database-id"

// WranglerExampleName and WranglerDevName are the file names of a role's config in edge/worker: the committed one with the
// placeholder id, and the git-ignored copy a local run makes from it.
func WranglerExampleName(role string) string { return "wrangler." + role + ".example.toml" }
func WranglerDevName(role string) string     { return "wrangler." + role + ".dev.toml" }

// wranglerEntry is the TypeScript entry of a role, relative to edge/worker.
func wranglerEntry(role string) string {
	if role == RoleFront {
		return "src/front.ts"
	}
	return "src/roles/" + role + ".ts"
}

var wranglerAbout = map[string]string{
	RoleFront:     "the router and the only entry from outside: the hostname, the Limiter and NodeLink objects, a service binding per role. No wasm, no secret.",
	RoleLink:      "the Go side of the agent link (enrolment, the steps of the link); FRONT and the NodeLink objects reach it by service binding.",
	RolePublic:    "subscriptions, the user page and the decoy; the hot path.",
	RoleAdmin:     "the admin SPA and API, sign-in and the first-run setup; the only role that creates the instance.",
	RoleJobs:      "the cron tick (expiry, node_down, the work of the background services) and nothing else; it takes no request.",
	RoleProvision: "the Provisioner Durable Object: SSH installs of nodes. A TCP socket belongs to the I/O context it was opened in, so Go serves only this object.",
}

// WranglerConfig renders the wrangler.toml of one role, with the D1 placeholder. Paths are relative to edge/worker; the assets
// directory is web/dist (build it first).
func (c EdgeConfig) WranglerConfig(role string) string {
	s := c.Script(role)
	if s == nil {
		panic("release: no script for role " + role)
	}
	script := func(role string) string { return c.Script(role).DefaultName }
	var b strings.Builder
	p := func(format string, a ...any) { fmt.Fprintf(&b, format+"\n", a...) }

	p("# Generated from internal/release.NewEdgeConfig (go generate ./internal/release). Do not edit: change the config and generate again.")
	p("# Copy to wrangler.%s.toml (or let scripts/build-edge.ps1 make the .dev.toml) and replace the database id; it holds no", role)
	p("# account id and no secret. Script %s: %s", s.DefaultName, wranglerAbout[role])
	p("name = %q", s.DefaultName)
	p("main = %q", wranglerEntry(role))
	p("compatibility_date = %q", c.CompatibilityDate)
	if len(c.CompatibilityFlags) > 0 {
		p("compatibility_flags = [%s]", quoteList(c.CompatibilityFlags))
	}
	p("workers_dev = false")

	if len(s.Cron) > 0 {
		p("\n[triggers]")
		p("crons = [%s]", quoteList(s.Cron))
	}
	for _, m := range s.Modules {
		if m.Type == "compiled_wasm" {
			p("\n# The Go panel of this role (dist/%s) is imported by src/roles/%s.ts as a precompiled WebAssembly module.", m.Name, role)
			p("[[rules]]\ntype = \"CompiledWasm\"\nglobs = [\"**/*.wasm\"]\nfallthrough = true")
		}
	}
	for _, d := range s.D1 {
		p("\n[[d1_databases]]\nbinding = %q\ndatabase_name = \"mistgate\"\ndatabase_id = %q", d.Binding, WranglerD1Placeholder)
	}
	for _, d := range s.DurableObjects {
		p("\n[[durable_objects.bindings]]\nname = %q\nclass_name = %q", d.Binding, d.ClassName)
	}
	for _, d := range s.ExternalObjects {
		p("\n[[durable_objects.bindings]]\nname = %q\nclass_name = %q\nscript_name = %q", d.Binding, d.ClassName, script(d.ScriptRole))
	}
	for _, sv := range s.Services {
		p("\n[[services]]\nbinding = %q\nservice = %q", sv.Binding, script(sv.ServiceRole))
		if sv.Entrypoint != "" {
			p("entrypoint = %q", sv.Entrypoint)
		}
	}
	for _, m := range s.Migrations {
		p("\n[[migrations]]\ntag = %q\nnew_sqlite_classes = [%s]", m.Tag, quoteList(m.NewSqliteClasses))
	}
	if s.Assets != nil {
		p("\n# Every request goes to the Worker first and the panel reads these files through the %s binding; only an exact file path is a hit.", s.Assets.Binding)
		p("[assets]\ndirectory = \"../../web/dist\"\nbinding = %q\nrun_worker_first = true\nhtml_handling = \"none\"\nnot_found_handling = \"none\"", s.Assets.Binding)
	}
	p("\n[observability]\nenabled = false")
	if len(s.Secrets) > 0 {
		p("\n# Secrets, never a value in this file: wrangler secret put <NAME> --name %s. Locally put NAME=value lines in the git-ignored", s.DefaultName)
		p("# .dev.vars. MASTER_KEY is 32 random bytes as 64 hex characters, the same in every script that has it.")
		p("#   %s", strings.Join(s.Secrets, ", "))
		if role == RoleAdmin {
			p("# Optional secret RECOVERY_TOKEN_SHA256 is set separately by `mistgate edge reset-login`.")
		}
	}
	if len(s.Vars) > 0 {
		p("\n# Optional first-run settings, read only while the database is empty (afterwards the database wins):")
		p("# [vars]")
		for _, v := range s.Vars {
			p("# %s = \"...\"", v)
		}
	}
	return b.String()
}

// WranglerFiles renders the committed config of every role, by file name.
func (c EdgeConfig) WranglerFiles() map[string]string {
	out := map[string]string{}
	for _, s := range c.Scripts {
		out[WranglerExampleName(s.Role)] = c.WranglerConfig(s.Role)
	}
	return out
}

func quoteList(items []string) string {
	q := make([]string, len(items))
	for i, it := range items {
		q[i] = fmt.Sprintf("%q", it)
	}
	return strings.Join(q, ", ")
}
