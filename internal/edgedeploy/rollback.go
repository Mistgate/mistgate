package edgedeploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/mistgate/mistgate/internal/release"
)

// rollback puts every script back on the deployment the key file recorded before the last bundle replaced it, in the reverse
// order of the upload (FRONT first, so the router stops sending traffic to roles before they change), and restores the cron
// schedules. D1 is not rolled back: a migration of a release only widens the schema, so the code of the release before still
// runs on it. A script the last bundle created has nothing recorded and stays as it is.
func rollback(ctx context.Context, opts Options, state localState, out io.Writer) error {
	api := newCFClient(opts.APIBaseURL, opts.Token, opts.HTTPClient)
	accountID, _, err := locate(ctx, api, opts, state, true)
	if err != nil {
		return err
	}
	recorded := 0
	for _, script := range state.Scripts {
		if script.Previous != nil {
			recorded++
		}
	}
	if recorded == 0 {
		return errors.New("nothing to roll back: the local deployment file records no earlier version (the scripts had none before the last deploy)")
	}
	tags, err := api.scripts(ctx, accountID)
	if err != nil {
		return err
	}
	// What a rollback leaves on Cloudflare is not what the fingerprints describe, so the next deploy must upload everything.
	// This is written before the first change: a rollback that stops half way must not leave a skip behind.
	if !opts.DryRun {
		for role, script := range state.Scripts {
			script.Fingerprint = ""
			state.Scripts[role] = script
		}
		state.Steps = nil
		if err := replaceStateFile(opts.StatePath, state); err != nil {
			return fmt.Errorf("update local deployment file before the rollback: %w", err)
		}
	}
	verb := "restored"
	if opts.DryRun {
		verb = "would be restored"
	}
	for i := len(release.EdgeRoles) - 1; i >= 0; i-- {
		script := state.Scripts[release.EdgeRoles[i]]
		if script.Previous == nil {
			fmt.Fprintf(out, "  %s: no earlier version recorded, left as it is\n", script.Name)
			continue
		}
		if _, exists := tags[script.Name]; !exists {
			return fmt.Errorf("script %s no longer exists on Cloudflare; nothing to restore it on", script.Name)
		}
		current, err := api.schedules(ctx, accountID, script.Name)
		if err != nil {
			return err
		}
		restoreCron := !sameStrings(current, script.Previous.Crons)
		if !opts.DryRun {
			if err := api.setDeployment(ctx, accountID, script.Name, script.Previous.Versions); err != nil {
				return err
			}
			if restoreCron {
				if err := api.putSchedules(ctx, accountID, script.Name, script.Previous.Crons); err != nil {
					return err
				}
			}
		}
		fmt.Fprintf(out, "  %s: %s %s, schedule %s\n", script.Name, versionIDs(script.Previous.Versions), verb, scheduleNote(script.Previous.Crons, restoreCron))
	}
	if !opts.DryRun {
		fmt.Fprintln(out, "Rolled back. The D1 database was not touched. Run the deploy again to move to a bundle again.")
	}
	return nil
}

func sameStrings(a, b []string) bool {
	return slices.Equal(slices.Sorted(slices.Values(a)), slices.Sorted(slices.Values(b)))
}

func versionIDs(versions []deploymentVersion) string {
	ids := make([]string, 0, len(versions))
	for _, v := range versions {
		ids = append(ids, v.VersionID)
	}
	return strings.Join(ids, "+")
}

func scheduleNote(crons []string, changed bool) string {
	switch {
	case !changed:
		return "unchanged"
	case len(crons) == 0:
		return "removed"
	}
	return "set to " + strings.Join(crons, ", ")
}
