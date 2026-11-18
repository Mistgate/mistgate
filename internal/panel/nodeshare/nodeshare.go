// Package nodeshare converts how a node is handed out to groups between the admin API and the store.
package nodeshare

import (
	"errors"
	"strings"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// Choice reads the share of a request that makes a node: unset or UNSPECIFIED is the panel's own choice. INVALID_ARGUMENT for
// a mode with the wrong groups (see Valid).
func Choice(m *adminv1.NodeSharing) (store.NodeShareChoice, error) {
	if m == nil {
		return store.NodeShareChoice{}, nil
	}
	if m.GetMode() == adminv1.NodeShareMode_NODE_SHARE_MODE_UNSPECIFIED {
		if len(m.GetGroupIds()) > 0 {
			return store.NodeShareChoice{}, invalid("group_ids need the mode LISTED")
		}
		return store.NodeShareChoice{}, nil
	}
	return Valid(m.GetMode(), m.GetGroupIds())
}

// Valid is the store's choice for an explicit mode: ALL and NONE name no groups, LISTED names at least one.
func Valid(mode adminv1.NodeShareMode, groupIDs []string) (store.NodeShareChoice, error) {
	c := store.NodeShareChoice{}
	switch mode {
	case adminv1.NodeShareMode_NODE_SHARE_MODE_ALL:
		c.Mode = store.ShareAll
	case adminv1.NodeShareMode_NODE_SHARE_MODE_NONE:
		c.Mode = store.ShareNone
	case adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED:
		c.Mode = store.ShareListed
	default:
		return c, invalid("mode must be ALL, LISTED or NONE")
	}
	if c.Mode != store.ShareListed && len(groupIDs) > 0 {
		return c, invalid("group_ids go with the mode LISTED only")
	}
	if c.Mode == store.ShareListed && len(groupIDs) == 0 {
		return c, invalid("the mode LISTED needs at least one group")
	}
	for _, id := range groupIDs {
		if strings.TrimSpace(id) == "" || len(id) > 64 {
			return c, invalid("group_ids holds an empty or too long id")
		}
	}
	c.GroupIDs = groupIDs
	return c, nil
}

// Mode is the API's name of a store mode.
func Mode(mode string) adminv1.NodeShareMode {
	switch mode {
	case store.ShareAll:
		return adminv1.NodeShareMode_NODE_SHARE_MODE_ALL
	case store.ShareListed:
		return adminv1.NodeShareMode_NODE_SHARE_MODE_LISTED
	}
	return adminv1.NodeShareMode_NODE_SHARE_MODE_NONE
}

func invalid(msg string) error {
	return connect.NewError(connect.CodeInvalidArgument, errors.New(msg))
}
