//go:build !js

package store

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var transactionShape1 = map[string]string{
	"admin.go:PutSetupToken":                       "Replaces unused setup tokens and inserts the latest in one fixed batch.",
	"access_user.go:CreateUser":                    "Inserts the user, selected nodes, and optional implicit device credentials in one fixed batch.",
	"access_user.go:EnsureImplicitDevice":          "Idempotently inserts the implicit device and missing credentials, then reads back its live snapshot.",
	"access_profile.go:CreateInbound":              "Inserts the inbound and clears its retained key in one fixed batch.",
	"access_profile.go:CreateGroup":                "Computes an automatic color in the insert and replaces the profile set in one fixed batch.",
	"batch_native.go:runSQLBatch":                  "Runs one SQL transaction on the explicitly selected reader or writer pool.",
	"batch.go:retryGuarded":                        "Runs guarded batches and serializes local retries after a guard fails.",
	"fleet_node.go:NodeDesired":                    "The desired revision update and node_sent digest upsert run in one fixed batch.",
	"fleet_events.go:RecordDeviceLimitReached":     "A user-history event is inserted only when that user's hourly window has no matching event.",
	"health.go:OpenAlert":                          "A fixed batch reopens a recent alert and upserts through the active-alert unique index.",
	"health.go:PutDoctor":                          "A fixed batch applies the report replacement and its doctor-result upserts.",
	"health.go:RollupDaily":                        "One SQL INSERT SELECT groups finished samples and inserts daily rows idempotently.",
	"node_dns.go:SetNodeOptions":                   "Replaces a node's offered presets with the input rows in one fixed batch.",
	"provision.go:FinishCancelledNodeProvisionJob": "The cancellation event and guarded state transition run in one fixed batch.",
	"provision.go:RequeueNodeProvisionJobs":        "Set-based event inserts and state updates requeue interrupted jobs in one fixed batch.",
	"provision.go:createNodeProvisionJob":          "The job insert and optional queued event run in one fixed batch.",
	"store.go:SetSettings":                         "Upserts the supplied settings in one fixed batch.",
	"telegram.go:ClearTelegramBot":                 "Deletes links and the bot as one fixed batch.",
	"token.go:RevokeAPIToken":                      "Revokes the token and cancels its open plans in one fixed batch.",
}

var transactionShape2 = map[string]string{
	"access_user.go:UpdateUser":                    "A user guard precedes the update and optional node replacement in one atomic batch.",
	"access_awg.go:AddAWGDevice":                   "A guarded batch retries if the device limit, profile epoch, or candidate peer allocation changed.",
	"access_awg.go:RotateAWGDevice":                "A guarded batch retries if the live credential, selected peer, or profile epoch changed.",
	"access_awg.go:EnsureImplicitAWGCreds":         "Guarded batches retry when an implicit device, profile epoch, or candidate peer allocation changed.",
	"access_user.go:RevokeDevice":                  "A live-device guard precedes device, peer, and credential updates plus a returning user lookup.",
	"access_profile.go:UpdateProfile":              "A version guard precedes the profile and optional epoch and inbound bumps.",
	"access_profile.go:DeleteProfile":              "A no-inbounds guard precedes device revocation and profile deletion.",
	"access_profile.go:DeleteInbound":              "A snapshot guard protects retained-key insertion and inbound deletion after the callback runs.",
	"access_profile.go:UpdateGroup":                "A group guard precedes its optional rename, field updates, and profile replacement.",
	"access_profile.go:DeleteGroup":                "A group and user guard or a group guard precedes its optional move and deletion.",
	"admin.go:DeletePasskey":                       "SQL guards the delete against removing an admin's last sign-in method.",
	"admin.go:createFirstAdmin":                    "A setup-token guard precedes the plain first-admin writes in one atomic batch.",
	"authceremony.go:FailAuthCeremony":             "A fixed batch increments one code failure and deletes the ceremony at the limit atomically.",
	"authpw.go:AddPassword":                        "A guarded insert checks account and login uniqueness in SQL.",
	"authpw.go:RecordLoginFailure":                 "The upsert computes lockout state in SQL and returns the resulting row.",
	"authpw.go:ResetPasswordLogin":                 "A credential guard precedes the plain credential, lock, session, and audit writes.",
	"awg_prepare.go:awgPrepareTx":                  "The exact JSON and backend read by fn guard its update; stale decisions retry.",
	"dns.go:Delete":                                "A preset guard precedes preset deletion and clearing user and group references.",
	"fleet_ca.go:CreateEnrollment":                 "Optional node creation, token replacement, and the current-node read share one batch guarded against missing or retired nodes.",
	"fleet_ca.go:Enroll":                           "A reader batch loads the token, node, and replay certificate; signing runs outside one guarded consume-and-issue batch.",
	"fleet_node.go:NodeHello":                      "Previous and current node rows bracket the guarded facts update and live-session replacement in one D1 batch.",
	"fleet_node.go:NodeDisconnected":               "The matching live session guards last-seen/disconnect time and row deletion in one fixed batch.",
	"fleet_node.go:RetireNode":                     "A node-state guard precedes retirement, certificate/token changes, provisioning cancellation, and retained-key deletion.",
	"fleet_stats.go:IngestEvent":                   "One batch conditionally inserts the event and advances the node sequence, distinguishing duplicates from missing nodes.",
	"fleet_stats.go:IngestStats":                   "Credential and node lookups run in one read batch; an eight-statement guarded write batch also updates the session-matched live projection.",
	"health.go:InsertProbeCredIdx":                 "A guarded batch retries when the inbound credential or candidate peer allocation changed.",
	"mcpplan.go:BeginApply":                        "A status and hash compare-and-swap lets only one apply proceed.",
	"mcpplan.go:CreateMCPPlan":                     "SQL guards per-token and panel-wide capacity in the atomic insert batch.",
	"mcpplan.go:decideMCPPlan":                     "A status compare-and-swap records one unexpired owner decision.",
	"provision.go:ClaimNodeProvisionJob":           "One ordered queued-state UPDATE RETURNING claims the job and batches its event.",
	"provision.go:CompleteNodeProvisionJob":        "A running-state guard precedes the completion, access, and event writes.",
	"provision.go:RequestCancelNodeProvisionJob":   "A state diagnostic and queued/running compare-and-swap keep cancellation events atomic.",
	"provision.go:updateNodeProvisionJobFromState": "The expected-state UPDATE is a compare-and-swap; its optional event shares the batch.",
	"telegram.go:BindTelegramChat":                 "SQL checks the admin while replacing both unique link owners.",
	"telegram.go:SetTelegramBot":                   "SQL drops links only when the stored bot differs, with the bot upsert in one batch.",
	"token.go:CreateAPIToken":                      "The insert guard enforces the live-token cap and unique active name in SQL.",
	"update.go:AddNodeToRunningRollout":            "A current-rollout guard precedes stage shifting, insertion, and schedule consumption.",
	"update.go:createRollout":                      "One SQL guard checks the rollout id, active slot, and optional exact schedule before fixed writes.",
	"warp.go:ReplaceWarpAccount":                   "An account-exists guard precedes the delete and replacement insert.",
}

var transactionShape3 = map[string]string{
	"fleet_ca.go:RenewCert":     "Writes only inside a transaction; D1 stages the writes and commits them as one batch.",
	"fleet_node.go:NodeApplied": "The node row, guarded live drift update, and json_each inbound results commit in one fixed three-statement batch.",
	"fleet_stats.go:SkipSeq":    "Writes only inside a transaction; D1 stages the sequence update and commits it as one batch.",
}

func TestTransactionCallSitesClassified(t *testing.T) {
	var sites []string
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			found := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if ok && (selector.Sel.Name == "BeginTx" || selector.Sel.Name == "batch" || selector.Sel.Name == "retryGuarded") {
					found = true
				}
				return true
			})
			if found {
				sites = append(sites, filepath.ToSlash(path)+":"+fn.Name.Name)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(sites)
	siteSet := make(map[string]bool, len(sites))
	for _, site := range sites {
		siteSet[site] = true
	}
	for _, list := range []map[string]string{transactionShape1, transactionShape2, transactionShape3} {
		for site := range list {
			if !siteSet[site] {
				t.Errorf("transaction classification %s is stale: no call site found", site)
			}
		}
	}
	var unclassified []string
	for _, site := range sites {
		count := 0
		for _, list := range []map[string]string{transactionShape1, transactionShape2, transactionShape3} {
			if reason, ok := list[site]; ok {
				count++
				if strings.TrimSpace(reason) == "" {
					t.Errorf("%s has an empty shape reason", site)
				}
			}
		}
		if count != 1 {
			unclassified = append(unclassified, fmt.Sprintf("%s (%d classifications)", site, count))
		}
	}
	if len(unclassified) != 0 {
		t.Fatalf("transaction call sites need exactly one shape classification:\n%s", strings.Join(unclassified, "\n"))
	}
}
