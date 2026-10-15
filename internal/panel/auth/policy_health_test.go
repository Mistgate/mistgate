package auth

import (
	"testing"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The roles of HealthService: everybody may read; owner and helper do the day-to-day work (mute, check now,
// run the doctor); only the owner applies a fix, because a fix changes the host or drops sessions.
func TestHealthServiceRolePolicy(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		path                  string
		owner, helper, readon bool
	}{
		{"ListAlerts", adminv1connect.HealthServiceListAlertsProcedure, true, true, true},
		{"GetChecks", adminv1connect.HealthServiceGetChecksProcedure, true, true, true},
		{"GetDoctor", adminv1connect.HealthServiceGetDoctorProcedure, true, true, true},
		{"MuteAlert", adminv1connect.HealthServiceMuteAlertProcedure, true, true, false},
		{"RunChecksNow", adminv1connect.HealthServiceRunChecksNowProcedure, true, true, false},
		{"RunDoctor", adminv1connect.HealthServiceRunDoctorProcedure, true, true, false},
		{"ApplyFix", adminv1connect.HealthServiceApplyFixProcedure, true, false, false},
		// the Reachability page's history: the country verdicts of GetChecks over time, read like them
		{"GetReachabilityHistory", adminv1connect.CountryReachabilityServiceGetReachabilityHistoryProcedure, true, true, true},
	} {
		if _, listed := procedureLevels[tc.path]; !listed {
			t.Errorf("%s has no explicit level (it would silently be owner-only)", tc.name)
		}
		l := levelOf(tc.path)
		for role, want := range map[string]bool{store.RoleOwner: tc.owner, store.RoleHelper: tc.helper, store.RoleReadonly: tc.readon} {
			if got := roleAllows(role, l); got != want {
				t.Errorf("%s as %s: allowed = %v, want %v", tc.name, role, got, want)
			}
		}
	}
}

// The history is open to tokens exactly as GetChecks is; the country itself stays the owner's and closed to them.
func TestReachabilityHistoryTokenAccessIsGetChecks(t *testing.T) {
	want, ok := TokenAccess(adminv1connect.HealthServiceGetChecksProcedure)
	if got, gotOK := TokenAccess(adminv1connect.CountryReachabilityServiceGetReachabilityHistoryProcedure); !ok || gotOK != ok || got != want {
		t.Errorf("TokenAccess(GetReachabilityHistory) = %d %v, GetChecks = %d %v", got, gotOK, want, ok)
	}
	if _, open := TokenAccess(adminv1connect.CountryReachabilityServiceGetCountryReachabilityProcedure); open {
		t.Error("GetCountryReachability is open to tokens")
	}
}
