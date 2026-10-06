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
	"admin.go:PutSetupToken":       "Replaces unused setup tokens and inserts the latest in one fixed batch.",
	"batch_native.go:batchStore":   "Runs fixed writes in one SQLite transaction and returns results after commit.",
	"telegram.go:ClearTelegramBot": "Deletes links and the bot as one fixed batch.",
	"token.go:RevokeAPIToken":      "Revokes the token and cancels its open plans in one fixed batch.",
}

var transactionShape2 = map[string]string{
	"admin.go:DeletePasskey":       "SQL guards the delete against removing an admin's last sign-in method.",
	"admin.go:createFirstAdmin":    "A setup-token guard precedes the plain first-admin writes in one atomic batch.",
	"authpw.go:AddPassword":        "A guarded insert checks account and login uniqueness in SQL.",
	"authpw.go:RecordLoginFailure": "The upsert computes lockout state in SQL and returns the resulting row.",
	"authpw.go:ResetPasswordLogin": "A credential guard precedes the plain credential, lock, session, and audit writes.",
	"token.go:CreateAPIToken":      "The insert guard enforces the live-token cap and unique active name in SQL.",
	"mcpplan.go:CreateMCPPlan":     "SQL guards per-token and panel-wide capacity in the atomic insert batch.",
	"mcpplan.go:BeginApply":        "A status and hash compare-and-swap lets only one apply proceed.",
	"mcpplan.go:decideMCPPlan":     "A status compare-and-swap records one unexpired owner decision.",
	"telegram.go:BindTelegramChat": "SQL checks the admin while replacing both unique link owners.",
	"telegram.go:SetTelegramBot":   "SQL drops links only when the stored bot differs, with the bot upsert in one batch.",
}

var transactionShape3 = map[string]string{
	"access.go:tx":                                 "not yet reviewed",
	"awg_prepare.go:awgPrepareTx":                  "not yet reviewed",
	"dns.go:Delete":                                "not yet reviewed",
	"fleet_ca.go:CreateEnrollment":                 "not yet reviewed",
	"fleet_ca.go:Enroll":                           "not yet reviewed",
	"fleet_ca.go:RenewCert":                        "not yet reviewed",
	"fleet_node.go:NodeApplied":                    "not yet reviewed",
	"fleet_node.go:NodeHello":                      "not yet reviewed",
	"fleet_node.go:RetireNode":                     "not yet reviewed",
	"fleet_stats.go:IngestEvent":                   "not yet reviewed",
	"fleet_stats.go:IngestStats":                   "not yet reviewed",
	"fleet_stats.go:SkipSeq":                       "not yet reviewed",
	"health.go:InsertProbeCredIdx":                 "not yet reviewed",
	"health.go:OpenAlert":                          "not yet reviewed",
	"health.go:PutDoctor":                          "not yet reviewed",
	"health.go:RollupDaily":                        "not yet reviewed",
	"node_dns.go:SetNodeOptions":                   "not yet reviewed",
	"provision.go:ClaimNodeProvisionJob":           "not yet reviewed",
	"provision.go:CompleteNodeProvisionJob":        "not yet reviewed",
	"provision.go:FinishCancelledNodeProvisionJob": "not yet reviewed",
	"provision.go:RequestCancelNodeProvisionJob":   "not yet reviewed",
	"provision.go:RequeueNodeProvisionJobs":        "not yet reviewed",
	"provision.go:createNodeProvisionJob":          "not yet reviewed",
	"provision.go:updateNodeProvisionJobFromState": "not yet reviewed",
	"store.go:SetSettings":                         "not yet reviewed",
	"update.go:AddNodeToRunningRollout":            "not yet reviewed",
	"update.go:createRollout":                      "not yet reviewed",
	"warp.go:ReplaceWarpAccount":                   "not yet reviewed",
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
				if ok && (selector.Sel.Name == "BeginTx" || selector.Sel.Name == "batch") {
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
