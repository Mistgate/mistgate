package access

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func setListUsersSort(m *adminv1.ListUsersRequest, sort uint64, descending bool) {
	msg := m.ProtoReflect()
	if field := msg.Descriptor().Fields().ByName("sort"); field != nil {
		msg.Set(field, protoreflect.ValueOfEnum(protoreflect.EnumNumber(sort)))
	} else {
		unknown := protowire.AppendTag(msg.GetUnknown(), 7, protowire.VarintType)
		unknown = protowire.AppendVarint(unknown, sort)
		msg.SetUnknown(unknown)
	}
	if field := msg.Descriptor().Fields().ByName("descending"); field != nil {
		msg.Set(field, protoreflect.ValueOfBool(descending))
	} else {
		unknown := protowire.AppendTag(msg.GetUnknown(), 8, protowire.VarintType)
		if descending {
			unknown = protowire.AppendVarint(unknown, 1)
		} else {
			unknown = protowire.AppendVarint(unknown, 0)
		}
		msg.SetUnknown(unknown)
	}
}

func listUserCountValue(counts *adminv1.UserCounts, name protoreflect.Name, number protowire.Number) uint32 {
	msg := counts.ProtoReflect()
	if field := msg.Descriptor().Fields().ByName(name); field != nil {
		return uint32(msg.Get(field).Uint())
	}
	value, _, _ := listUsersUnknownInteger(msg.GetUnknown(), number)
	return uint32(value)
}

func addListTestDevice(e *env, userID, deviceID string) {
	e.sql(`INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
		VALUES (?, ?, ?, ?, 0, ?)`, deviceID, userID, []byte("hwid-"+deviceID), e.clock.Unix(), e.clock.Unix())
	e.sql(`INSERT INTO device_credential (id, device_id, user_id, protocol, secret_enc, data_json, created_at)
		VALUES (?, ?, ?, 'hysteria2', ?, '{}', ?)`, "crd_"+deviceID, deviceID, userID, []byte("sealed"), e.clock.Unix())
}

func addListTestAWGDevice(e *env, userID, profileID, deviceID string, lastSeen int64, idx int) {
	e.sql(`INSERT INTO device (id, user_id, hwid_hash, first_seen_at, last_seen_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`, deviceID, userID, []byte("awg-"+deviceID), lastSeen, lastSeen, e.clock.Unix())
	credentialID := "crd_" + deviceID
	e.sql(`INSERT INTO device_credential (id, device_id, user_id, protocol, profile_id, secret_enc, data_json, created_at)
		VALUES (?, ?, ?, 'awg', ?, ?, '{}', ?)`, credentialID, deviceID, userID, profileID, []byte("sealed"), e.clock.Unix())
	e.sql(`INSERT INTO awg_peer (credential_id, profile_id, idx, public_key, created_at)
		VALUES (?, ?, ?, ?, ?)`, credentialID, profileID, idx, "public-"+deviceID, e.clock.Unix())
}

func TestListUsersSortsBothDirections(t *testing.T) {
	f := newFixture(t)
	e := f.e
	gAlpha := e.group("alpha", f.profile)
	gZulu := e.group("zulu", f.profile)
	now := e.clock.Unix()

	users := []struct {
		name, group, status string
		used, expires, seen int64
		quota               int64
		limit, extraDevices int
	}{
		{name: "ada", group: gZulu, status: "active", used: 20, expires: 0, seen: 0, limit: 5, extraDevices: 2},
		{name: "bob", group: gAlpha, status: "active", used: 10, expires: now + 10*86400, seen: now - 2*86400, limit: 9, extraDevices: 1},
		{name: "cy", group: gZulu, status: "limited", used: 20, expires: now + 5*86400, seen: now - 10*86400, quota: 10, limit: 2, extraDevices: 1},
		{name: "dan", group: gAlpha, status: "expired", used: 40, expires: now - 2*86400, seen: now - 86400, limit: 3},
		{name: "erin", group: gZulu, status: "disabled", used: 10, expires: now + 5*86400, seen: 0, limit: 8, extraDevices: 2},
		{name: "fox", group: gAlpha, status: "active", used: 30, expires: now + 86400, seen: now - 20*86400, limit: 1},
		{name: "gail", group: gZulu, status: "active", used: 20, expires: now + 86400, seen: now - 3*86400, limit: 2, extraDevices: 1},
	}
	ids := map[string]string{}
	for _, u := range users {
		created := e.user(u.name, u.group, nil).User
		ids[u.name] = created.Id
		for i := 0; i < u.extraDevices; i++ {
			addListTestDevice(e, created.Id, fmt.Sprintf("dev_%s_%d", u.name, i))
		}
		disabled := 0
		if u.status == "disabled" {
			disabled = 1
		}
		var expires any
		if u.expires != 0 {
			expires = u.expires
		}
		e.sql(`UPDATE user SET status = ?, disabled = ?, used_bytes = ?, quota_bytes = ?, expires_at = ?, last_seen_at = ?, device_limit = ? WHERE id = ?`,
			u.status, disabled, u.used, u.quota, expires, u.seen, u.limit, created.Id)
	}
	e.online[ids["bob"]] = "nod_de1"
	e.online[ids["cy"]] = "nod_de1"
	e.online[ids["fox"]] = "nod_de1"

	checks := []struct {
		name       string
		sort       uint64
		ascending  string
		descending string
	}{
		{"name", 1, "ada,bob,cy,dan,erin,fox,gail", "gail,fox,erin,dan,cy,bob,ada"},
		{"traffic", 2, "bob,erin,ada,cy,gail,fox,dan", "dan,fox,ada,cy,gail,bob,erin"},
		{"expiry", 3, "dan,fox,gail,cy,erin,bob,ada", "ada,bob,cy,erin,fox,gail,dan"},
		{"last seen", 4, "ada,erin,gail,dan,bob,cy,fox", "bob,cy,fox,dan,gail,ada,erin"},
		{"devices", 5, "fox,dan,cy,gail,bob,ada,erin", "erin,ada,bob,cy,gail,dan,fox"},
		{"group", 6, "bob,dan,fox,ada,cy,erin,gail", "ada,cy,erin,gail,bob,dan,fox"},
		{"status", 7, "ada,bob,fox,gail,cy,dan,erin", "erin,dan,cy,fox,gail,ada,bob"},
	}
	for _, check := range checks {
		for _, dir := range []struct {
			desc bool
			want string
		}{{false, check.ascending}, {true, check.descending}} {
			t.Run(check.name+map[bool]string{false: "/asc", true: "/desc"}[dir.desc], func(t *testing.T) {
				m := &adminv1.ListUsersRequest{PageSize: 100}
				setListUsersSort(m, check.sort, dir.desc)
				got := must(e.s.ListUsers(e.ctx, req(m))).Msg
				if names := userListNames(got.Users); names != dir.want {
					rows := make([]string, 0, len(got.Users))
					for _, user := range got.Users {
						rows = append(rows, user.Name+":"+user.Status.String()+":"+fmt.Sprint(user.DevicesUsed, "/", user.DeviceLimit))
					}
					t.Fatalf("users = %s (%s), want %s", names, strings.Join(rows, ","), dir.want)
				}
			})
		}
	}

	// Non-name orders use offset paging, return no name cursor, and cover each row once across pages.
	var paged []string
	for _, offset := range []uint32{0, 3, 6} {
		m := &adminv1.ListUsersRequest{PageSize: 3, Offset: offset}
		setListUsersSort(m, 2, false)
		page := must(e.s.ListUsers(e.ctx, req(m))).Msg
		if page.NextPageToken != "" {
			t.Fatalf("traffic page at offset %d returned a name token %q", offset, page.NextPageToken)
		}
		for _, u := range page.Users {
			paged = append(paged, u.Name)
		}
	}
	if got, want := strings.Join(paged, ","), "bob,erin,ada,cy,gail,fox,dan"; got != want {
		t.Fatalf("traffic pages = %s, want %s", got, want)
	}
	seen := map[string]bool{}
	for _, name := range paged {
		if seen[name] {
			t.Fatalf("traffic pages returned %q twice", name)
		}
		seen[name] = true
	}
	if len(seen) != len(users) {
		t.Fatalf("traffic pages returned %d unique rows, want %d", len(seen), len(users))
	}

	// Filters, group_id and query continue to apply under a non-name sort; counts still ignore filter.
	m := &adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_ONLINE, GroupId: gAlpha, Query: "alpha", PageSize: 10}
	setListUsersSort(m, 2, true)
	filtered := must(e.s.ListUsers(e.ctx, req(m))).Msg
	if got := userListNames(filtered.Users); got != "fox,bob" {
		t.Fatalf("filtered traffic order = %s, want fox,bob", got)
	}
	if filtered.Counts.All != 3 || listUserCountValue(filtered.Counts, "devices_used", 5) != 4 ||
		listUserCountValue(filtered.Counts, "devices_online", 6) != 3 {
		t.Fatalf("query/group counts = %+v, devices=(%d,%d)", filtered.Counts,
			listUserCountValue(filtered.Counts, "devices_used", 5), listUserCountValue(filtered.Counts, "devices_online", 6))
	}

	token := &adminv1.ListUsersRequest{PageToken: base64.RawURLEncoding.EncodeToString([]byte("ada"))}
	setListUsersSort(token, 2, false)
	_, err := e.s.ListUsers(e.ctx, req(token))
	wantCode(t, err, connect.CodeInvalidArgument)
	badSort := &adminv1.ListUsersRequest{}
	setListUsersSort(badSort, 99, false)
	_, err = e.s.ListUsers(e.ctx, req(badSort))
	wantCode(t, err, connect.CodeInvalidArgument)

	// New name cursors are bound to their direction; old plain-name cursors remain ascending-only.
	nameAsc := &adminv1.ListUsersRequest{PageSize: 3}
	setListUsersSort(nameAsc, 1, false)
	ascPage := must(e.s.ListUsers(e.ctx, req(nameAsc))).Msg
	if ascPage.NextPageToken == "" {
		t.Fatal("ascending name page has no cursor")
	}
	opposite := &adminv1.ListUsersRequest{PageSize: 3, PageToken: ascPage.NextPageToken}
	setListUsersSort(opposite, 1, true)
	_, err = e.s.ListUsers(e.ctx, req(opposite))
	wantCode(t, err, connect.CodeInvalidArgument)
	legacy := &adminv1.ListUsersRequest{PageSize: 3, PageToken: base64.RawURLEncoding.EncodeToString([]byte("ada"))}
	setListUsersSort(legacy, 1, false)
	if got := userListNames(must(e.s.ListUsers(e.ctx, req(legacy))).Msg.Users); got != "bob,cy,dan" {
		t.Fatalf("legacy ascending cursor returned %s, want bob,cy,dan", got)
	}

	// Name order retains keyset paging in descending direction.
	var descendingPages []string
	descCursor := ""
	tok := ""
	for range 4 {
		m := &adminv1.ListUsersRequest{PageSize: 3, PageToken: tok}
		setListUsersSort(m, 1, true)
		page := must(e.s.ListUsers(e.ctx, req(m))).Msg
		if len(descendingPages) == 0 {
			descCursor = page.NextPageToken
		}
		for _, u := range page.Users {
			descendingPages = append(descendingPages, u.Name)
		}
		if tok = page.NextPageToken; tok == "" {
			break
		}
	}
	if got, want := strings.Join(descendingPages, ","), "gail,fox,erin,dan,cy,bob,ada"; got != want {
		t.Fatalf("descending name keyset pages = %s, want %s", got, want)
	}
	if descCursor == "" {
		t.Fatal("descending name page has no cursor")
	}
	opposite = &adminv1.ListUsersRequest{PageSize: 3, PageToken: descCursor}
	setListUsersSort(opposite, 1, false)
	_, err = e.s.ListUsers(e.ctx, req(opposite))
	wantCode(t, err, connect.CodeInvalidArgument)
	legacyDescending := &adminv1.ListUsersRequest{PageSize: 3, PageToken: base64.RawURLEncoding.EncodeToString([]byte("ada"))}
	setListUsersSort(legacyDescending, 1, true)
	_, err = e.s.ListUsers(e.ctx, req(legacyDescending))
	wantCode(t, err, connect.CodeInvalidArgument)
}

func TestListUsersDeviceCounts(t *testing.T) {
	f := newFixture(t)
	e := f.e
	offline := e.user("offline", f.group, nil).User
	online := e.user("online", f.group, nil).User
	awgProfile := e.awgProfile("list devices", "")
	awgGroup := e.group("awg devices", awgProfile.Id)
	awgFresh := e.user("awg-fresh", awgGroup, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Amnezia: true}
	}).User
	awgStale := e.user("awg-stale", awgGroup, func(m *adminv1.CreateUserRequest) {
		m.Apps = &adminv1.AppToggles{Amnezia: true}
	}).User
	addListTestDevice(e, offline.Id, "dev_offline")
	addListTestDevice(e, online.Id, "dev_online")
	addListTestAWGDevice(e, awgFresh.Id, awgProfile.Id, "dev_awg_fresh", e.clock.Add(-60_000_000_000).Unix(), 2)
	addListTestAWGDevice(e, awgStale.Id, awgProfile.Id, "dev_awg_stale", e.clock.Add(-4*60_000_000_000).Unix(), 3)
	e.online[online.Id] = "nod_de1"
	e.online[awgStale.Id] = "nod_de1" // an AWG handshake, not the user's session, controls this device's online flag

	filtered := &adminv1.ListUsersRequest{Filter: adminv1.UserFilter_USER_FILTER_ONLINE}
	response := must(e.s.ListUsers(e.ctx, req(filtered))).Msg
	if response.Counts.All != 4 || response.Counts.Online != 2 {
		t.Fatalf("people counts = %+v, want all=4 online=2", response.Counts)
	}
	if got := listUserCountValue(response.Counts, "devices_used", 5); got != 6 {
		t.Fatalf("devices used = %d, want 6", got)
	}
	if got := listUserCountValue(response.Counts, "devices_online", 6); got != 3 {
		t.Fatalf("devices online = %d, want 3", got)
	}
	wire := must(proto.Marshal(response))
	var roundTrip adminv1.ListUsersResponse
	if err := proto.Unmarshal(wire, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if got := listUserCountValue(roundTrip.Counts, "devices_used", 5); got != 6 {
		t.Fatalf("wire devices used = %d, want 6", got)
	}
	if got := listUserCountValue(roundTrip.Counts, "devices_online", 6); got != 3 {
		t.Fatalf("wire devices online = %d, want 3", got)
	}
}

func userListNames(users []*adminv1.User) string {
	names := make([]string, 0, len(users))
	for _, user := range users {
		names = append(names, user.Name)
	}
	return strings.Join(names, ",")
}
