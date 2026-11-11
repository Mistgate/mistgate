//go:build !js

package mcp

import (
	"strings"
	"testing"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// outdated_devices: the counts of every state, the page worst-first as the panel sent it, the app string cleaned as data, the
// filters passed on, and a state the tool does not know refused before the panel is asked.
func TestOutdatedDevicesView(t *testing.T) {
	e := newTestEnv(t)
	_, secret := e.token(ProfileReadonly)
	s := e.session(secret)

	v := decode[OutdatedDevicesV](t, mustOK(t, s, "outdated_devices", map[string]any{}))
	if v.Total != 3 || len(v.Devices) != 3 || v.HasMore || len(v.Counts) != 4 || v.Counts[0].State != "ok" || v.Counts[0].Devices != 7 || v.Counts[0].People != 4 {
		t.Fatalf("view = %+v", v)
	}
	if d := v.Devices[0]; d.State != "unsupported" || d.Reason != "app" || d.UserName != "alice" || d.App == "" {
		t.Errorf("first = %+v", d)
	}
	if d := v.Devices[1]; d.State != "below_minimum" || d.NeedVersion != "3.1.1" || strings.Contains(d.Client, canaryTK) || strings.Contains(d.Client, "\n") {
		t.Errorf("hostile client string not cleaned: %+v", d)
	}
	if len(v.Fingerprints) != 1 || v.Fingerprints[0] != "chrome" {
		t.Errorf("fingerprints = %v", v.Fingerprints)
	}
	// what the nodes saw: the class and the counts per device, the classes over all devices, the disagreements, the nodes that do not count
	if d := v.Devices[0]; d.Actual != "classic" || d.HelloPQ != 0 || d.HelloClassic != 12 || d.HelloUnknown != 1 || d.TableMismatch != "" {
		t.Errorf("measured counts of the first device: %+v", d)
	}
	if d := v.Devices[1]; d.Actual != "pq" || d.HelloPQ != 41 || d.TableMismatch != "better" {
		t.Errorf("measured counts of the second device: %+v", d)
	}
	if d := v.Devices[2]; d.Actual != "none" || d.HelloPQ+d.HelloClassic+d.HelloUnknown != 0 {
		t.Errorf("a device nobody measured: %+v", d)
	}
	if len(v.Actual) != 4 || v.Actual[0].Class != "none" || v.Actual[0].Devices != 5 || v.Actual[1].Class != "pq" || v.Actual[2].Class != "classic" || v.Actual[2].Devices != 1 ||
		v.Actual[3].Class != "mixed" || v.HelloWindowHours != 168 || v.MismatchWorse != 2 || v.MismatchBetter != 1 {
		t.Errorf("summary of the measured classes: %+v", v)
	}
	if len(v.HelloKexNodesWithout) != 2 || v.HelloKexNodesWithout[0] != "nl1" || strings.Contains(v.HelloKexNodesWithout[1], canaryTK) {
		t.Errorf("nodes that do not count: %v", v.HelloKexNodesWithout)
	}

	one := decode[OutdatedDevicesV](t, mustOK(t, s, "outdated_devices", map[string]any{"state": "unknown", "user_id": "usr_bob", "limit": 10}))
	if len(one.Devices) != 1 || one.Devices[0].DeviceID != "dev_3" || !one.Devices[0].Shared {
		t.Errorf("filtered = %+v", one.Devices)
	}
	last := e.w.compatReq[len(e.w.compatReq)-1]
	if last.State != adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN || last.UserId != "usr_bob" || last.PageSize != 10 {
		t.Errorf("request = %v", last)
	}

	byClass := decode[OutdatedDevicesV](t, mustOK(t, s, "outdated_devices", map[string]any{"actual": "classic"}))
	if len(byClass.Devices) != 1 || byClass.Devices[0].DeviceID != "dev_1" {
		t.Errorf("by measured class = %+v", byClass.Devices)
	}
	if last := e.w.compatReq[len(e.w.compatReq)-1]; last.Actual != adminv1.ClientCompatActual_CLIENT_COMPAT_ACTUAL_CLASSIC || last.State != 0 {
		t.Errorf("request of the class filter = %v", last)
	}

	mustFail(t, s, "outdated_devices", map[string]any{"actual": "quantum"})
	mustFail(t, s, "outdated_devices", map[string]any{"state": "outdated"})
	mustFail(t, s, "outdated_devices", map[string]any{"user_id": "usr bob; drop"})
}

func TestOutdatedDevicesPaging(t *testing.T) {
	r := &adminv1.ListOutdatedDevicesResponse{Total: 7, Devices: []*adminv1.OutdatedDevice{{DeviceId: "dev_a"}, {DeviceId: "dev_b"}}}
	v := outdatedDevicesView(r, 4)
	if !v.HasMore || v.NextOffset != 6 {
		t.Errorf("middle page: has_more %v next %d", v.HasMore, v.NextOffset)
	}
	v = outdatedDevicesView(r, 5)
	if v.HasMore || v.NextOffset != 0 {
		t.Errorf("last page: has_more %v next %d", v.HasMore, v.NextOffset)
	}
	v = outdatedDevicesView(r, 0)
	v.shrink() // a shortened page continues where it now ends
	if len(v.Devices) != 1 || !v.HasMore || v.NextOffset != 1 || !v.Truncated {
		t.Errorf("shrunk: %+v", v)
	}
}
