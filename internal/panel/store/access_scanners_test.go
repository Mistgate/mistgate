package store

import (
	"reflect"
	"testing"
	"time"
)

func TestSharedAccessScannersHandleNullColumns(t *testing.T) {
	inboundRow := []any{
		"inb_null", "prf_null", "nod_null", nil, nil, nil, int64(7), nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil,
		"prf_null", nil, nil, nil, nil, nil, nil, nil,
		"nod_null", nil, nil, nil, nil, nil, nil, nil,
	}
	full, err := scanAccessInboundFull(batchRow(inboundRow))
	if err != nil {
		t.Fatal(err)
	}
	wantFull := AccessInboundFull{
		Inbound: AccessInbound{ID: "inb_null", ProfileID: "prf_null", NodeID: "nod_null", SpecVersion: 7},
		Profile: AccessProfile{ID: "prf_null"},
		Node:    AccessNode{ID: "nod_null"},
	}
	if !reflect.DeepEqual(full, wantFull) {
		t.Fatalf("scanAccessInboundFull = %#v, want %#v", full, wantFull)
	}

	awgRow := []any{
		"dev_null", "usr_null", nil, nil, nil, nil, nil, nil,
		"prf_null", nil, nil, "crd_null", nil, nil, nil, nil, nil, nil, nil,
		"prf_null", nil, nil, nil, nil, nil, nil, nil,
	}
	var profile AccessProfile
	device, err := scanAWGDevice(batchRow(awgRow), &profile)
	if err != nil {
		t.Fatal(err)
	}
	wantDevice := AccessAWGDevice{
		AccessDevice: AccessDevice{ID: "dev_null", UserID: "usr_null", AWG: true, Protocols: []string{"awg"}},
		ProfileID:    "prf_null",
		CredID:       "crd_null",
	}
	if !reflect.DeepEqual(device, wantDevice) {
		t.Fatalf("scanAWGDevice = %#v, want %#v", device, wantDevice)
	}
	if !reflect.DeepEqual(profile, AccessProfile{ID: "prf_null"}) {
		t.Fatalf("scanAWGDevice profile = %#v", profile)
	}

	group, err := scanAccessGroup(batchRow([]any{"grp_null", nil, nil, nil, nil, nil}))
	if err != nil {
		t.Fatal(err)
	}
	wantGroup := AccessGroup{ID: "grp_null"}
	if !reflect.DeepEqual(group, wantGroup) {
		t.Fatalf("scanAccessGroup = %#v, want %#v", group, wantGroup)
	}
	if !full.Inbound.CreatedAt.IsZero() || !device.CreatedAt.IsZero() || !group.CreatedAt.Equal(time.Time{}) {
		t.Fatal("NULL timestamps did not map to zero time")
	}
}
