package nodeshare

import (
	"testing"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestChoice(t *testing.T) {
	const (
		all    = adminv1.NodeShareMode_NODE_SHARE_MODE_ALL
		listed = adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED
		none   = adminv1.NodeShareMode_NODE_SHARE_MODE_NONE
		auto   = adminv1.NodeShareMode_NODE_SHARE_MODE_UNSPECIFIED
	)
	for _, c := range []struct {
		name   string
		in     *adminv1.NodeSharing
		want   store.NodeShareChoice
		refuse bool
	}{
		{"unset is the panel's choice", nil, store.NodeShareChoice{}, false},
		{"unspecified is the panel's choice", &adminv1.NodeSharing{Mode: auto}, store.NodeShareChoice{}, false},
		{"groups need a mode", &adminv1.NodeSharing{Mode: auto, GroupIds: []string{"grp_a"}}, store.NodeShareChoice{}, true},
		{"all", &adminv1.NodeSharing{Mode: all}, store.NodeShareChoice{Mode: store.ShareAll}, false},
		{"none", &adminv1.NodeSharing{Mode: none}, store.NodeShareChoice{Mode: store.ShareNone}, false},
		{"listed", &adminv1.NodeSharing{Mode: listed, GroupIds: []string{"grp_a"}}, store.NodeShareChoice{Mode: store.ShareListed, GroupIDs: []string{"grp_a"}}, false},
		{"all with groups", &adminv1.NodeSharing{Mode: all, GroupIds: []string{"grp_a"}}, store.NodeShareChoice{}, true},
		{"none with groups", &adminv1.NodeSharing{Mode: none, GroupIds: []string{"grp_a"}}, store.NodeShareChoice{}, true},
		{"listed with none", &adminv1.NodeSharing{Mode: listed}, store.NodeShareChoice{}, true},
		{"listed with an empty id", &adminv1.NodeSharing{Mode: listed, GroupIds: []string{" "}}, store.NodeShareChoice{}, true},
	} {
		got, err := Choice(c.in)
		if c.refuse != (err != nil) || c.refuse && connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%s: err %v", c.name, err)
		}
		if !c.refuse && (got.Mode != c.want.Mode || len(got.GroupIDs) != len(c.want.GroupIDs)) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	for _, mode := range []string{store.ShareAll, store.ShareListed, store.ShareNone} {
		if Mode(mode) == auto {
			t.Errorf("mode %s has no API name", mode)
		}
	}
}
