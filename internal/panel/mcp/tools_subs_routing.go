//go:build !js

package mcp

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// The owner's own routing profiles for Happ and INCY (SubscriptionSettings.routing_profiles). subscription_settings_get lists them
// (counts, DNS, groups, size and, for a small one, the JSON), subscription_routing_set changes one through a plan. The profile is read
// by the apps of every person of its groups and decides what goes outside the VPN, so the card names the groups, the counts of the
// rules, the hosts the apps download geo files from and where the link goes, and never the rules themselves.

const (
	// routingInlineBytes: a profile at most this long is shown whole in subscription_settings_get; a longer one is shown by its counts and hash.
	routingInlineBytes  = 4 << 10
	maxRoutingHostFacts = 10
)

// routingProfileV is one profile as the settings read shows it.
type routingProfileV struct {
	Name        string   `json:"name"`
	AllGroups   bool     `json:"all_groups"`
	GroupIDs    []string `json:"group_ids"`
	Mode        string   `json:"mode"` // add_and_turn_on | add_only
	Bytes       int      `json:"bytes"`
	SHA256      string   `json:"sha256_12"`
	DirectSites int      `json:"direct_sites"`
	DirectIP    int      `json:"direct_ip"`
	ProxySites  int      `json:"proxy_sites"`
	ProxyIP     int      `json:"proxy_ip"`
	BlockSites  int      `json:"block_sites"`
	BlockIP     int      `json:"block_ip"`
	RemoteDNS   string   `json:"remote_dns,omitempty"`
	DomesticDNS string   `json:"domestic_dns,omitempty"`
	GeoFiles    bool     `json:"geo_files"`
	InBody      bool     `json:"in_body"` // the link is too long for the `routing` header and goes on a line of the subscription body
	Profile     string   `json:"profile,omitempty"`
	Lossy       bool     `json:"lossy,omitempty"`
}

func routingModeWord(onlyAdd bool) string {
	if onlyAdd {
		return "add_only"
	}
	return "add_and_turn_on"
}

func routingProfileViews(s *adminv1.SubscriptionSettings) []routingProfileV {
	out := make([]routingProfileV, 0, len(s.GetRoutingProfiles()))
	for _, e := range s.GetRoutingProfiles() {
		info, err := subsettings.RoutingInfoOf(e.GetProfile(), nil)
		if err != nil {
			continue
		}
		v := routingProfileV{
			Name: nm(info.Name), AllGroups: !e.GetOnlyGroups(), GroupIDs: cleanIDs(e.GetGroupIds()), Mode: routingModeWord(e.GetOnlyAdd()),
			Bytes: len(e.GetProfile()), SHA256: hashText(e.GetProfile())[:12],
			DirectSites: int(info.DirectSites), DirectIP: int(info.DirectIp), ProxySites: int(info.ProxySites), ProxyIP: int(info.ProxyIp),
			BlockSites: int(info.BlockSites), BlockIP: int(info.BlockIp),
			RemoteDNS: dnsWord(info.RemoteDnsType, info.RemoteDns), DomesticDNS: dnsWord(info.DomesticDnsType, info.DomesticDns),
			GeoFiles: info.GeoipAddress != "" || info.GeositeAddress != "", InBody: info.HeaderBytes > subsettings.MaxRoutingHeaderValue,
		}
		if len(e.GetProfile()) <= routingInlineBytes {
			if scrubbed := scrub(e.GetProfile()); scrubbed == e.GetProfile() {
				v.Profile = scrubbed
			} else {
				v.Lossy = true
			}
		}
		out = append(out, v)
	}
	return out
}

func dnsWord(kind, addr string) string {
	if addr == "" {
		return ""
	}
	return clean(kind+" "+addr, 200)
}

// routingUseV says which profile the person's Happ and INCY answers carry.
type routingUseV struct {
	Profile string `json:"profile"` // the name; "" = none of the owner's: the apps get the person's DNS preset as a profile
	Reason  string `json:"reason,omitempty"`
	Note    string `json:"note"`
}

func routingUseOf(s *adminv1.SubscriptionSettings, u *adminv1.User) *routingUseV {
	v := &routingUseV{Note: "Only the link-list answer to Happ and INCY carries a routing profile (the `routing` header, or a line of the body when the link is too long); every other app and format is the same for the whole instance."}
	e := subsettings.RoutingProfileFor(s, u.GetGroupId())
	switch {
	case e == nil:
		v.Reason = "no_profile_for_group"
	case u.GetStatus() != adminv1.UserStatus_USER_STATUS_ACTIVE:
		v.Reason = "no_access"
	default:
		if d, ok := subsettings.RoutingDocOf(e); ok {
			v.Profile = nm(d.Name)
		}
	}
	return v
}

// ---------------------------------------------------------------------------------------------------------------------
// subscription_routing_set

type routingSetArgs struct {
	ReasonField
	Name         string    `json:"name" jsonschema:"the profile's name, 1 to 60 characters, unique ignoring case; it is also the Name the apps show (it replaces the Name inside the pasted profile). A name that is not in subscription_settings_get creates a profile"`
	Profile      *string   `json:"profile,omitempty" jsonschema:"what the owner pasted: a link (happ://routing/onadd/..., incy://..., ://routing/add/...), the base64 of the JSON, or the JSON itself (at most 128 KiB of text, 32 KiB once read). Only the fields the apps document are kept; the rest are dropped. Required for a new profile; omitted: the rules stay. Text read through subscription_settings_get with lossy true holds markers such as [redacted] and is refused"`
	Mode         *string   `json:"mode,omitempty" jsonschema:"add_and_turn_on (the app adds the profile and turns it on) or add_only (the person turns it on). Omitted: what a pasted link says (add or onadd), else the profile's current mode, else add_and_turn_on"`
	GroupIDs     *[]string `json:"group_ids,omitempty" jsonschema:"the complete list of the ids of the groups whose people get this profile, by their main group (a group has one profile at most); [] gives it to nobody; omitted: the groups stay. Not together with all_groups"`
	AllGroups    *bool     `json:"all_groups,omitempty" jsonschema:"true: every group gets this profile unless a profile names the group (one profile at most is for all groups); false needs group_ids"`
	Remove       bool      `json:"remove,omitempty" jsonschema:"delete the profile: its people get the DNS profile of their preset again; give nothing else with it"`
	SettingsHash string    `json:"settings_hash,omitempty" jsonschema:"-"`
}

type routingChange struct {
	next          *adminv1.SubscriptionSettings
	before, after *adminv1.RoutingProfile // before: nil for a new profile; after: nil for a removed one
	beforeName    string
	dropped       []string
}

func routingEntryName(e *adminv1.RoutingProfile) string {
	d, _ := subsettings.RoutingDocOf(e)
	return d.Name
}

func (a routingSetArgs) build(cur *adminv1.SubscriptionSettings, groups map[string]string) (*routingChange, error) {
	name := strings.TrimSpace(a.Name)
	if name == "" {
		return nil, errors.New("name is required")
	}
	if err := visible("name", name); err != nil {
		return nil, err
	}
	if a.Remove && (a.Profile != nil || a.Mode != nil || a.GroupIDs != nil || a.AllGroups != nil) {
		return nil, errors.New("remove stands alone: give no profile, mode or groups with it")
	}
	if !a.Remove && a.Profile == nil && a.Mode == nil && a.GroupIDs == nil && a.AllGroups == nil {
		return nil, errors.New("give profile, mode, group_ids, all_groups or remove")
	}
	if a.GroupIDs != nil && a.AllGroups != nil && *a.AllGroups {
		return nil, errors.New("group_ids and all_groups: give one of them")
	}
	if a.AllGroups != nil && !*a.AllGroups && a.GroupIDs == nil {
		return nil, errors.New("all_groups false needs group_ids")
	}
	next := proto.Clone(cur).(*adminv1.SubscriptionSettings)
	at := slices.IndexFunc(next.RoutingProfiles, func(e *adminv1.RoutingProfile) bool { return strings.EqualFold(routingEntryName(e), name) })
	ch := &routingChange{next: next}
	if at >= 0 {
		ch.before = proto.Clone(next.RoutingProfiles[at]).(*adminv1.RoutingProfile)
		ch.beforeName = routingEntryName(ch.before)
	}
	if a.Remove {
		if at < 0 {
			return nil, errors.New("no routing profile has this name: the names are in subscription_settings_get")
		}
		next.RoutingProfiles = slices.Delete(next.RoutingProfiles, at, at+1)
		return ch, nil
	}
	if at < 0 && a.Profile == nil {
		return nil, errors.New("a new profile needs profile: no routing profile has this name yet")
	}
	e := &adminv1.RoutingProfile{OnlyGroups: true}
	if at >= 0 {
		e = next.RoutingProfiles[at]
	}
	if a.Profile != nil {
		if err := visible("profile", *a.Profile); err != nil {
			return nil, err
		}
		if err := refuseRedacted(*a.Profile); err != nil {
			return nil, err
		}
		p, err := subsettings.ParseRoutingInput(*a.Profile)
		if err != nil {
			return nil, errors.New(strings.TrimPrefix(err.Error(), subsettings.ErrInvalid.Error()+": "))
		}
		p.Doc.Name = name
		if ch.beforeName != "" {
			p.Doc.Name = ch.beforeName // found ignoring case: the profile keeps the name it has
		}
		e.Profile = p.Doc.Canonical()
		ch.dropped = p.Dropped
		if a.Mode == nil && p.Mode != "" {
			e.OnlyAdd = p.Mode == "add"
		}
	}
	if a.Mode != nil {
		switch *a.Mode {
		case "add_and_turn_on":
			e.OnlyAdd = false
		case "add_only":
			e.OnlyAdd = true
		default:
			return nil, errors.New("mode: add_and_turn_on or add_only")
		}
	}
	if a.AllGroups != nil && *a.AllGroups {
		e.OnlyGroups, e.GroupIds = false, nil
	}
	if a.GroupIDs != nil {
		ids, err := uniqueIDs("group_ids", *a.GroupIDs, true)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if _, ok := groups[id]; !ok {
				return nil, fmt.Errorf("group_ids: no group with id %s (group ids are in groups_list)", clean(id, 60))
			}
		}
		e.OnlyGroups, e.GroupIds = true, ids
	}
	if at < 0 {
		next.RoutingProfiles = append(next.RoutingProfiles, e)
	}
	ch.after = e
	return ch, nil
}

var routingSet = changeSpec[routingSetArgs]{
	name: "subscription_routing_set", min: ProfileOperator, danger: true, maxArgs: maxMihomoArgsJSON,
	procs: slices.Concat(settingsProcs, procs(adminv1connect.GroupServiceListGroupsProcedure)),
	desc: "Create, change or delete one own routing profile for the apps Happ and INCY and the groups whose people get it (by their main group; the answer of their subscription link carries it as the `routing` header, or as a line of the body when the link is too long). " +
		"Give name and profile (what the owner pasted: a happ://routing/onadd/... or incy://... link, the base64 of the JSON, or the JSON) and/or mode, group_ids or all_groups, or name and remove. " +
		"The profile is read as the admin's save reads it: only the fields the apps document are kept, the rest is dropped, a wrong type or a geo-file address that is not https is refused. The person's own DNS preset replaces the DNS fields of the profile; the rules stay the owner's. " +
		"The owner's card shows the name, groups, mode, the counts of the rules, the hosts the apps download from and where the link goes, not the rules; this always waits for approval.",
	plan: func(c *call, a routingSetArgs) (*planned, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return nil, err
		}
		groups, err := c.groupNames()
		if err != nil {
			return nil, err
		}
		ch, err := a.build(cur.settings, groups)
		if err != nil {
			return nil, err
		}
		if slices.EqualFunc(cur.settings.RoutingProfiles, ch.next.RoutingProfiles, func(x, y *adminv1.RoutingProfile) bool { return proto.Equal(x, y) }) {
			return nil, errors.New("nothing to change: the profile already looks like this")
		}
		if err := checked(ch.next); err != nil { // normalized and validated whole, as a save does
			return nil, err
		}
		facts := routingFacts(ch, groups)
		if ch.after != nil {
			a.Name = routingEntryName(ch.after)
			p, mode := ch.after.Profile, routingModeWord(ch.after.OnlyAdd) // the stored plan carries the profile as it will be saved, not the pasted text
			a.Profile = &p
			a.Mode = &mode // a link's own mode is in the pasted text only
		} else {
			a.Name = ch.beforeName
		}
		a.SettingsHash = cur.hash
		summary := "Change one own routing profile of the shared subscription settings. The owner must approve this change."
		if ch.after == nil {
			summary = "Delete one own routing profile: the people of its groups get the DNS profile of their preset again. The owner must approve this change."
		}
		return &planned{Summary: summary, Facts: facts, Danger: []string{dangerPage}, Params: a}, nil
	},
	apply: func(c *call, a routingSetArgs, _ Plan) (done, error) {
		cur, err := c.subscriptionSnapshot()
		if err != nil {
			return done{}, err
		}
		if cur.hash != a.SettingsHash {
			return done{}, changedSincePlan()
		}
		groups, err := c.groupNames()
		if err != nil {
			return done{}, err
		}
		ch, err := a.build(cur.settings, groups)
		if err != nil { // a group of the plan was deleted since, or the stored plan was cut
			return done{}, failure("changed_since_plan", "the plan no longer applies ("+clean(err.Error(), 160)+"): make a new plan")
		}
		if err := c.saveSubscriptionSettings(a.SettingsHash, ch.next); err != nil {
			return done{}, err
		}
		return doneWith("subscription_routing_updated", "Routing profile saved."), nil
	},
}

// routingFacts is the owner's card: facts in the panel's words, the values that come from the agent or from the profile marked untrusted.
func routingFacts(ch *routingChange, groups map[string]string) []Fact {
	e, action := ch.after, "update"
	switch {
	case ch.before == nil:
		action = "create"
	case ch.after == nil:
		e, action = ch.before, "remove"
	}
	name := routingEntryName(e)
	facts := []Fact{{Key: "routing_profile", Value: nm(name), Untrusted: true}, {Key: "action", Value: action}}
	if action == "update" && (ch.before.OnlyGroups != e.OnlyGroups || !slices.Equal(ch.before.GroupIds, e.GroupIds)) {
		facts = append(facts, Fact{Key: "groups_before", Value: routingGroups(ch.before, groups).Value, Untrusted: true})
	}
	facts = append(facts, routingGroups(e, groups))
	if action == "remove" {
		return append(facts, codedFact("effect", "the people of these groups get the DNS profile of their preset again", "routing_removed"))
	}
	if action == "update" && ch.before.OnlyAdd != e.OnlyAdd {
		facts = append(facts, changeFact("mode", routingModeWord(ch.before.OnlyAdd)+" -> "+routingModeWord(e.OnlyAdd), routingModeWord(ch.before.OnlyAdd), routingModeWord(e.OnlyAdd)))
	} else if e.OnlyAdd {
		facts = append(facts, codedFact("mode", "the app adds the profile; the person turns it on", "routing_add"))
	} else {
		facts = append(facts, codedFact("mode", "the app adds the profile and turns it on", "routing_onadd"))
	}
	info, err := subsettings.RoutingInfoOf(e.Profile, nil)
	if err != nil {
		return facts
	}
	n := func(v uint32) string { return strconv.FormatUint(uint64(v), 10) }
	facts = append(facts, codedFact("rules", fmt.Sprintf("direct: %d sites, %d addresses; through the VPN: %d sites, %d addresses; blocked: %d sites, %d addresses",
		info.DirectSites, info.DirectIp, info.ProxySites, info.ProxyIp, info.BlockSites, info.BlockIp), "routing_rules",
		"direct_sites", n(info.DirectSites), "direct_ip", n(info.DirectIp), "proxy_sites", n(info.ProxySites), "proxy_ip", n(info.ProxyIp), "block_sites", n(info.BlockSites), "block_ip", n(info.BlockIp)))
	if action == "update" && ch.before.Profile != e.Profile {
		facts = append(facts, changeFact("bytes", strconv.Itoa(len(ch.before.Profile))+" -> "+strconv.Itoa(len(e.Profile)), strconv.Itoa(len(ch.before.Profile)), strconv.Itoa(len(e.Profile))),
			changeFact("sha256_12", hashText(ch.before.Profile)[:12]+" -> "+hashText(e.Profile)[:12], hashText(ch.before.Profile)[:12], hashText(e.Profile)[:12]))
	} else {
		facts = append(facts, Fact{Key: "bytes", Value: n(info.ProfileBytes)}, Fact{Key: "sha256_12", Value: hashText(e.Profile)[:12]})
	}
	if info.HeaderBytes > subsettings.MaxRoutingHeaderValue {
		facts = append(facts, codedFact("delivery", "a line of the subscription body (the link is too long for the header)", "routing_in_body", "bytes", n(info.HeaderBytes)))
	} else {
		facts = append(facts, codedFact("delivery", "the `routing` header", "routing_in_header", "bytes", n(info.HeaderBytes)))
	}
	hosts := routingHosts(e.Profile)
	shown := hosts[:min(len(hosts), maxRoutingHostFacts)]
	text := "none"
	if len(shown) > 0 {
		text = strings.Join(shown, ", ")
		if len(hosts) > len(shown) {
			text += fmt.Sprintf(" (and %d more)", len(hosts)-len(shown))
		}
	}
	facts = append(facts, Fact{Key: "download_hosts", Value: text, Untrusted: true})
	if len(ch.dropped) > 0 {
		facts = append(facts, Fact{Key: "warnings", Value: "fields left out: " + clean(strings.Join(ch.dropped, ", "), 200), Untrusted: true})
	}
	return append(facts, codedFact("effect", "the people of these groups get this profile in the link answer to Happ and INCY, and the person's DNS preset replaces its DNS fields; the rules are not shown here", "routing_profile"))
}

func routingGroups(e *adminv1.RoutingProfile, names map[string]string) Fact {
	switch {
	case !e.GetOnlyGroups():
		return codedFact("groups", "all groups (unless a profile names the group)", "all_groups")
	case len(e.GetGroupIds()) == 0:
		return codedFact("groups", "none: nobody gets the profile", "nobody_groups")
	}
	return Fact{Key: "groups", Value: groupList(e.GetGroupIds(), names), Untrusted: true}
}

// routingHosts are the hosts the apps of the people will fetch geo files and DoH answers from, from the profile: sorted, each once.
func routingHosts(profile string) []string {
	p, err := subsettings.ParseRoutingInput(profile)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, raw := range []string{p.Doc.Geoipurl, p.Doc.Geositeurl, p.Doc.RemoteDNSDomain, p.Doc.DomesticDNSDomain} {
		if u, err := url.Parse(raw); err == nil && u.Host != "" {
			if h := clean(strings.ToLower(u.Host), maxHostChars); !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	slices.Sort(out)
	return out
}
