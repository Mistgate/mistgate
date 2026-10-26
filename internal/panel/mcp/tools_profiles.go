package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
)

const profileSecretMask = "••••"

func profileTools() []toolDef { return slices.Concat(profileReadTools(), profileChangeTools()) }

func profileReadTools() []toolDef {
	return []toolDef{
		readTool("protocols_list", ProfileReadonly, procs(adminv1connect.ProfileServiceListProtocolsProcedure),
			"List protocol plugins, their client apps, compact settings schemas, and default settings. Secret fields in schemas and defaults are masked.",
			func(c *call, _ struct{}) (any, error) {
				r, err := c.cl.Profile.ListProtocols(c.ctx, connect.NewRequest(&adminv1.ListProtocolsRequest{}))
				if err != nil {
					return nil, apiError(err)
				}
				out := protocolsListV{Protocols: []protocolV{}}
				for _, p := range r.Msg.GetProtocols() {
					schema, err := parseSettingsSchema(p.GetSettingsSchemaJson())
					if err != nil {
						return nil, err
					}
					defaults, err := maskedSettings(schema, p.GetDefaultSettingsJson())
					if err != nil {
						return nil, errors.New("the protocol defaults are unavailable")
					}
					pv := protocolV{ID: clean(p.GetId(), 40), Name: nm(p.GetDisplayName()), Apps: []string{}, SettingsSchema: compactSchema(schema), DefaultSettings: defaults}
					for _, app := range p.GetApps() {
						pv.Apps = append(pv.Apps, protocolAppName(app))
					}
					out.Protocols = append(out.Protocols, pv)
				}
				return out, nil
			}),
		readTool("profiles_list", ProfileReadonly, procs(adminv1connect.ProfileServiceListProfilesProcedure, adminv1connect.GroupServiceListGroupsProcedure),
			"List profiles with their protocol, summary, deployment and user counts, warnings, subscription formats, version, and groups that give access.",
			func(c *call, _ struct{}) (any, error) {
				return c.profileList()
			}),
		readTool("profile_get", ProfileReadonly, procs(adminv1connect.ProfileServiceGetProfileProcedure, adminv1connect.GroupServiceListGroupsProcedure),
			"Get one profile's summary, masked settings, inbounds with node names, effective and override ports and server names, enabled state, and groups that give access.",
			func(c *call, in profileGetArgs) (any, error) {
				if err := validID("profile_id", in.ProfileID); err != nil {
					return nil, err
				}
				r, err := c.cl.Profile.GetProfile(c.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: in.ProfileID}))
				if err != nil {
					return nil, apiError(err)
				}
				groups, err := c.profileGroups()
				if err != nil {
					return nil, err
				}
				protocols, err := c.protocols()
				if err != nil {
					return nil, err
				}
				p := r.Msg.GetProfile()
				if p == nil {
					return nil, errors.New("profile is unavailable")
				}
				schema, ok := protocols[p.GetProtocol()]
				if !ok {
					return nil, errors.New("profile protocol is unavailable")
				}
				settings, err := maskedSettings(schema, r.Msg.GetSettingsJson())
				if err != nil {
					return nil, errors.New("profile settings are unavailable")
				}
				inbounds := make([]profileInboundV, 0, len(r.Msg.GetInbounds()))
				for _, ib := range r.Msg.GetInbounds() {
					inbounds = append(inbounds, profileInboundV{
						ID: ib.GetId(), NodeID: ib.GetNodeId(), NodeName: nm(ib.GetNodeName()), Port: ib.GetPort(),
						TLSServerName: clean(ib.GetTlsServerName(), 253),
						PortOverride:  ib.GetPortOverride(), TLSServerNameOverride: clean(ib.GetTlsServerNameOverride(), 253),
						Enabled: ib.GetEnabled(), State: enumName("INBOUND_STATE_", ib.GetState().String()),
					})
				}
				return profileGetV{Profile: profileSummaryView(p, groups[p.GetId()]), Settings: settings, Inbounds: inbounds,
					GroupIDs: slices.Clone(groups[p.GetId()])}, nil
			}),
	}
}

type profileGetArgs struct {
	ProfileID string `json:"profile_id" jsonschema:"profile id from profiles_list"`
}

type protocolsListV struct {
	Protocols []protocolV `json:"protocols"`
}

type protocolV struct {
	ID              string           `json:"id"`
	Name            string           `json:"name"`
	Apps            []string         `json:"apps"`
	SettingsSchema  []map[string]any `json:"settings_schema"`
	DefaultSettings json.RawMessage  `json:"default_settings"`
}

type profileSummaryV struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Protocol  string            `json:"protocol"`
	Summary   string            `json:"summary"`
	NodeCount uint32            `json:"node_count"`
	UserCount uint32            `json:"user_count"`
	Warnings  []profileWarningV `json:"warnings"`
	Formats   []profileFormatV  `json:"formats"`
	Version   uint32            `json:"version"`
	GroupIDs  []string          `json:"group_ids"`
}

type profileWarningV struct {
	Code   string            `json:"code"`
	Params map[string]string `json:"params,omitempty"`
}

type profileListV struct {
	Profiles []profileSummaryV `json:"profiles"`
}

type profileGetV struct {
	Profile  profileSummaryV   `json:"profile"`
	Settings json.RawMessage   `json:"settings"`
	Inbounds []profileInboundV `json:"inbounds"`
	GroupIDs []string          `json:"group_ids"`
}

type profileInboundV struct {
	ID                    string `json:"id"`
	NodeID                string `json:"node_id"`
	NodeName              string `json:"node_name"`
	Port                  uint32 `json:"port"`
	TLSServerName         string `json:"tls_server_name,omitempty"`
	PortOverride          uint32 `json:"port_override"`
	TLSServerNameOverride string `json:"tls_server_name_override,omitempty"`
	Enabled               bool   `json:"enabled"`
	State                 string `json:"state"`
}

func profileSummaryView(p *adminv1.ProfileSummary, groups []string) profileSummaryV {
	v := profileSummaryV{
		ID: p.GetId(), Name: nm(p.GetName()), Protocol: clean(p.GetProtocol(), 40), Summary: clean(p.GetSummary(), 300),
		NodeCount: p.GetNodeCount(), UserCount: p.GetUserCount(), Warnings: profileWarnings(p.GetWarnings()), Formats: []profileFormatV{},
		Version: p.GetVersion(), GroupIDs: slices.Clone(groups),
	}
	for _, f := range p.GetFormats() {
		v.Formats = append(v.Formats, profileFormatV{Format: subFormat(f.GetFormat()), OK: f.GetOk(), Note: clean(f.GetNote(), 60)})
	}
	return v
}

func (c *call) profileList() (profileListV, error) {
	r, err := c.cl.Profile.ListProfiles(c.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{}))
	if err != nil {
		return profileListV{}, apiError(err)
	}
	groups, err := c.profileGroups()
	if err != nil {
		return profileListV{}, err
	}
	out := profileListV{Profiles: make([]profileSummaryV, 0, len(r.Msg.GetProfiles()))}
	for _, p := range r.Msg.GetProfiles() {
		out.Profiles = append(out.Profiles, profileSummaryView(p, groups[p.GetId()]))
	}
	return out, nil
}

func (c *call) profileGroups() (map[string][]string, error) {
	r, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	out := map[string][]string{}
	for _, g := range r.Msg.GetGroups() {
		for _, id := range g.GetProfileIds() {
			out[id] = append(out[id], g.GetId())
		}
	}
	for id := range out {
		slices.Sort(out[id])
	}
	return out, nil
}

func (c *call) protocols() (map[string]map[string]any, error) {
	r, err := c.cl.Profile.ListProtocols(c.ctx, connect.NewRequest(&adminv1.ListProtocolsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	out := make(map[string]map[string]any, len(r.Msg.GetProtocols()))
	for _, p := range r.Msg.GetProtocols() {
		schema, err := parseSettingsSchema(p.GetSettingsSchemaJson())
		if err != nil {
			return nil, err
		}
		out[p.GetId()] = schema
	}
	return out, nil
}

func protocolAppName(a adminv1.App) string {
	switch a {
	case adminv1.App_APP_HAPP:
		return "happ"
	case adminv1.App_APP_AMNEZIA:
		return "amnezia"
	default:
		return enumName("APP_", a.String())
	}
}

func parseSettingsSchema(raw string) (map[string]any, error) {
	var schema map[string]any
	if err := json.Unmarshal([]byte(raw), &schema); err != nil || schema == nil {
		return nil, errors.New("the protocol settings schema is unavailable")
	}
	return schema, nil
}

func compactSchema(schema map[string]any) []map[string]any {
	var out []map[string]any
	var walk func(map[string]any, string)
	walk = func(parent map[string]any, prefix string) {
		props, _ := parent["properties"].(map[string]any)
		keys := make([]string, 0, len(props))
		for key := range props {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child, _ := props[key].(map[string]any)
			path := prefix + "/" + escapeJSONPointer(key)
			field := map[string]any{"path": path, "x-critical": child["x-critical"] == true, "x-secret": child["x-secret"] == true}
			if value, ok := child["type"]; ok {
				field["type"] = value
			}
			if child["x-secret"] != true {
				for in, to := range map[string]string{"enum": "enum", "minimum": "min", "maximum": "max"} {
					if value, ok := child[in]; ok {
						field[to] = value
					}
				}
			}
			if value, ok := child["default"]; ok {
				if child["x-secret"] == true {
					value = profileSecretMask
				} else {
					value = maskedDefault(child, value)
				}
				field["default"] = value
			}
			out = append(out, field)
			walk(child, path)
			if items, ok := child["items"].(map[string]any); ok {
				walk(items, path+"/*")
			}
		}
	}
	walk(schema, "")
	return out
}

func maskedDefault(schema map[string]any, value any) any {
	b, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var copy any
	if json.Unmarshal(b, &copy) != nil {
		return value
	}
	maskSchemaSecrets(schema, copy)
	return copy
}

func decodeSettings(raw string) (map[string]any, error) {
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	var settings map[string]any
	if err := dec.Decode(&settings); err != nil || settings == nil {
		return nil, errors.New("settings must be a JSON object")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errors.New("settings must contain one JSON object")
	}
	return settings, nil
}

func maskedSettings(schema map[string]any, raw string) (json.RawMessage, error) {
	settings, err := decodeSettings(raw)
	if err != nil {
		return nil, err
	}
	maskSchemaSecrets(schema, settings)
	b, err := json.Marshal(settings)
	return b, err
}

func maskSchemaSecrets(schema map[string]any, value any) {
	object, ok := value.(map[string]any)
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for key, raw := range props {
		child, _ := raw.(map[string]any)
		v, exists := object[key]
		if !exists {
			continue
		}
		if child["x-secret"] == true {
			object[key] = profileSecretMask
			continue
		}
		if nested, ok := v.(map[string]any); ok {
			maskSchemaSecrets(child, nested)
		}
		if array, ok := v.([]any); ok {
			if itemSchema, ok := child["items"].(map[string]any); ok {
				for _, item := range array {
					maskSchemaSecrets(itemSchema, item)
				}
			}
		}
	}
}

func validateSecretInput(schema map[string]any, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	settings, err := decodeSettings(string(raw))
	if err != nil {
		return err
	}
	var walk func(map[string]any, map[string]any, string) error
	walk = func(schema map[string]any, values map[string]any, prefix string) error {
		props, _ := schema["properties"].(map[string]any)
		for key, childRaw := range props {
			value, present := values[key]
			if !present {
				continue
			}
			child, _ := childRaw.(map[string]any)
			path := prefix + "/" + escapeJSONPointer(key)
			if child["x-secret"] == true {
				s, ok := value.(string)
				if !ok || (s != profileSecretMask && s != "$generate") {
					return fmt.Errorf("secret field %s must be omitted, %q, or %q", path, profileSecretMask, "$generate")
				}
				continue
			}
			if nested, ok := value.(map[string]any); ok {
				if err := walk(child, nested, path); err != nil {
					return err
				}
			}
			if array, ok := value.([]any); ok {
				if itemSchema, ok := child["items"].(map[string]any); ok {
					for i, item := range array {
						if nested, ok := item.(map[string]any); ok {
							if err := walk(itemSchema, nested, path+"/"+strconv.Itoa(i)); err != nil {
								return err
							}
						}
					}
				}
			}
		}
		return nil
	}
	return walk(schema, settings, "")
}

func mergePatch(target, patch any) any {
	patchObject, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	result := map[string]any{}
	if targetObject, ok := target.(map[string]any); ok {
		for key, value := range targetObject {
			result[key] = value
		}
	}
	for key, value := range patchObject {
		if value == nil {
			delete(result, key)
			continue
		}
		result[key] = mergePatch(result[key], value)
	}
	return result
}

func settingsPatchHasNull(value any) bool {
	switch value := value.(type) {
	case nil:
		return true
	case map[string]any:
		for _, child := range value {
			if settingsPatchHasNull(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if settingsPatchHasNull(child) {
				return true
			}
		}
	}
	return false
}

func isNotFound(err error) bool {
	var ce *connect.Error
	return errors.As(err, &ce) && ce.Code() == connect.CodeNotFound
}

func profileChangeTools() []toolDef {
	var out []toolDef
	for _, specs := range [][]toolDef{change(profileCreate), change(profileUpdate), change(profileDeploy)} {
		out = append(out, specs...)
	}
	return out
}

type profileGroupSnapshot struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	UserCount uint32 `json:"user_count"`
}

func groupSnapshots(groups []*adminv1.Group, ids []string) ([]profileGroupSnapshot, error) {
	byID := make(map[string]*adminv1.Group, len(groups))
	for _, g := range groups {
		byID[g.GetId()] = g
	}
	out := make([]profileGroupSnapshot, 0, len(ids))
	for _, id := range ids {
		g := byID[id]
		if g == nil {
			return nil, errors.New("group not found")
		}
		out = append(out, snapshotGroup(g))
	}
	return out, nil
}

func snapshotGroup(g *adminv1.Group) profileGroupSnapshot {
	return profileGroupSnapshot{ID: g.GetId(), Name: nm(g.GetName()), UserCount: g.GetUserCount()}
}

func (c *call) getGroups() ([]*adminv1.Group, error) {
	r, err := c.cl.Group.ListGroups(c.ctx, connect.NewRequest(&adminv1.ListGroupsRequest{}))
	if err != nil {
		return nil, apiError(err)
	}
	return r.Msg.GetGroups(), nil
}

func findGroup(groups []*adminv1.Group, id string) *adminv1.Group {
	for _, g := range groups {
		if g.GetId() == id {
			return g
		}
	}
	return nil
}

func (c *call) groupProfileIDs(id string) ([]string, error) {
	groups, err := c.getGroups()
	if err != nil {
		return nil, err
	}
	g := findGroup(groups, id)
	if g == nil {
		return nil, failure("changed_since_plan", "a selected group no longer exists; make a new plan")
	}
	return slices.Clone(g.GetProfileIds()), nil
}

func profileFormatsFacts(formats []*adminv1.ProfileFormat) []Fact {
	out := make([]Fact, 0, len(formats))
	for _, f := range formats {
		value := subFormat(f.GetFormat())
		if f.GetNote() != "" {
			value += ": " + clean(f.GetNote(), 60)
		} else if f.GetOk() {
			value += ": full"
		} else {
			value += ": unavailable"
		}
		out = append(out, Fact{Key: "format", Value: value})
	}
	return out
}

func profileWarnings(in []*adminv1.StatusReason) []profileWarningV {
	out := make([]profileWarningV, 0, len(in))
	for _, w := range in {
		params := make(map[string]string, len(w.GetParams()))
		for k, value := range w.GetParams() {
			params[clean(k, 40)] = clean(value, 160)
		}
		out = append(out, profileWarningV{Code: clean(w.GetCode(), 60), Params: params})
	}
	return out
}

func warningText(w []profileWarningV) string {
	if len(w) == 0 {
		return "none"
	}
	var out []string
	for _, v := range w {
		out = append(out, v.Code)
	}
	return strings.Join(out, ", ")
}

func profileCreatePortOverrides(nodes []string, overrides map[string]uint32) (map[string]uint32, error) {
	out := map[string]uint32{}
	for id, port := range overrides {
		if err := validID("port_overrides node id", id); err != nil {
			return nil, err
		}
		if !slices.Contains(nodes, id) {
			return nil, errors.New("port_overrides may name only selected nodes")
		}
		if port == 0 || port > 65535 {
			return nil, errors.New("port override must be 1-65535")
		}
		out[id] = port
	}
	return out, nil
}

type profileCreateArgs struct {
	ReasonField
	Protocol       string            `json:"protocol" jsonschema:"protocol id from protocols_list"`
	Name           string            `json:"name" jsonschema:"profile name, unique ignoring case"`
	Settings       map[string]any    `json:"settings,omitempty" jsonschema:"optional JSON settings object; missing fields use protocol defaults; x-secret fields may only be omitted, masked, or $generate"`
	NodeIDs        []string          `json:"node_ids,omitempty" jsonschema:"nodes to deploy the new profile on"`
	GroupIDs       []string          `json:"group_ids,omitempty" jsonschema:"groups that should receive the new profile"`
	PortOverrides  map[string]uint32 `json:"port_overrides,omitempty" jsonschema:"node id to port override, only for selected nodes"`
	AllowLossyPort bool              `json:"allow_lossy_port,omitempty" jsonschema:"save a port despite a lossy UDP delivery check; this requires owner approval"`
}

func checkProfileNameAvailable(c *call, name, exceptID string) error {
	profiles, err := c.cl.Profile.ListProfiles(c.ctx, connect.NewRequest(&adminv1.ListProfilesRequest{}))
	if err != nil {
		return apiError(err)
	}
	for _, p := range profiles.Msg.GetProfiles() {
		if p.GetId() != exceptID && strings.EqualFold(p.GetName(), name) {
			return errors.New("a profile with this name already exists")
		}
	}
	return nil
}

var profileCreateProcs = procs(
	adminv1connect.ProfileServiceListProtocolsProcedure, adminv1connect.ProfileServiceListProfilesProcedure,
	adminv1connect.ProfileServicePreviewProfileProcedure, adminv1connect.ProfileServiceCreateProfileProcedure,
	adminv1connect.ProfileServiceCreateInboundProcedure, adminv1connect.ProfileServiceDeleteInboundProcedure,
	adminv1connect.ProfileServiceDeleteProfileProcedure, adminv1connect.NodeServiceListNodesProcedure,
	adminv1connect.GroupServiceListGroupsProcedure, adminv1connect.GroupServiceUpdateGroupProcedure,
)

var profileCreate = changeSpec[profileCreateArgs]{
	name: "profile_create", min: ProfileAdmin, danger: true, procs: profileCreateProcs,
	desc: "Create a protocol profile, optionally deploy it on nodes and add it to groups. The plan validates settings and every selected node and reports any lossy-port override; every change needs the owner's approval.",
	plan: func(c *call, a profileCreateArgs) (*planned, error) {
		if err := checkName(a.Name); err != nil {
			return nil, err
		}
		if err := validID("protocol", a.Protocol); err != nil {
			return nil, err
		}
		protocols, err := c.protocols()
		if err != nil {
			return nil, err
		}
		schema, ok := protocols[a.Protocol]
		if !ok {
			return nil, errors.New("unknown protocol")
		}
		settings := a.Settings
		if len(settings) == 0 {
			settings = map[string]any{}
		}
		settingsJSON, err := json.Marshal(settings)
		if err != nil {
			return nil, errors.New("settings could not be read")
		}
		if err := validateSecretInput(schema, settingsJSON); err != nil {
			return nil, err
		}
		settingsObj, err := decodeSettings(string(settingsJSON))
		if err != nil {
			return nil, err
		}
		settingsJSON, err = json.Marshal(settingsObj)
		if err != nil {
			return nil, errors.New("settings could not be read")
		}
		if err := checkProfileNameAvailable(c, a.Name, ""); err != nil {
			return nil, err
		}
		preview, err := c.cl.Profile.PreviewProfile(c.ctx, connect.NewRequest(&adminv1.PreviewProfileRequest{Protocol: a.Protocol, SettingsJson: string(settingsJSON)}))
		if err != nil {
			return nil, apiError(err)
		}
		if len(preview.Msg.GetErrors()) > 0 {
			f := preview.Msg.GetErrors()[0]
			return nil, failure(clean(f.GetCode(), 60), "invalid settings at "+clean(f.GetPointer(), 120)+": "+clean(f.GetMessage(), 200))
		}
		nodeIDs, err := uniqueIDs("node_ids", a.NodeIDs, true)
		if err != nil {
			return nil, err
		}
		groupIDs, err := uniqueIDs("group_ids", a.GroupIDs, true)
		if err != nil {
			return nil, err
		}
		portOverrides, err := profileCreatePortOverrides(nodeIDs, a.PortOverrides)
		if err != nil {
			return nil, err
		}
		nodeList, err := c.cl.Node.ListNodes(c.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		nodeNames := map[string]string{}
		for _, n := range nodeList.Msg.GetNodes() {
			nodeNames[n.GetId()] = n.GetName()
		}
		groupList, err := c.getGroups()
		if err != nil {
			return nil, err
		}
		groups, err := groupSnapshots(groupList, groupIDs)
		if err != nil {
			return nil, err
		}
		for _, n := range nodeIDs {
			if nodeNames[n] == "" {
				return nil, errors.New("node not found")
			}
		}
		facts := []Fact{{Key: "profile_name", Value: nm(a.Name), Untrusted: true},
			{Key: "profile_summary", Value: clean(preview.Msg.GetSummary(), 300), Untrusted: true}}
		facts = append(facts, profileFormatsFacts(preview.Msg.GetFormats())...)
		for _, id := range nodeIDs {
			validated, err := c.cl.Profile.CreateInbound(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CreateInboundRequest{
				NodeId: id, Protocol: a.Protocol, SettingsJson: string(settingsJSON), PortOverride: portOverrides[id],
				ValidateOnly: true, AllowLossyPort: a.AllowLossyPort,
			}))
			if err != nil {
				return nil, apiError(err)
			}
			warnings := profileWarnings(validated.Msg.GetWarnings())
			facts = append(facts, Fact{Key: "node", Value: nm(nodeNames[id]), Untrusted: true},
				Fact{Key: "port", Value: strconv.FormatUint(uint64(validated.Msg.GetInbound().GetPort()), 10)},
				Fact{Key: "warnings", Value: warningText(warnings)})
		}
		for _, g := range groups {
			facts = append(facts, Fact{Key: "group", Value: g.Name, Untrusted: true}, Fact{Key: "users", Value: strconv.FormatUint(uint64(g.UserCount), 10)})
		}
		a.Settings, a.NodeIDs, a.GroupIDs, a.PortOverrides = settingsObj, nodeIDs, groupIDs, portOverrides
		return &planned{Summary: "Create one protocol profile and make the selected node and group placements.", Facts: facts, Danger: []string{dangerFleet}, Params: a,
			AuditParams: map[string]any{"protocol": a.Protocol, "name": a.Name, "settings": a.Settings, "node_ids": a.NodeIDs,
				"group_ids": a.GroupIDs, "port_overrides": a.PortOverrides, "allow_lossy_port": a.AllowLossyPort}}, nil
	},
	apply: func(c *call, a profileCreateArgs, _ Plan) (done, error) {
		protocols, err := c.protocols()
		if err != nil {
			return done{}, err
		}
		schema, ok := protocols[a.Protocol]
		if !ok {
			return done{}, errors.New("profile protocol is unavailable")
		}
		if a.Settings == nil {
			a.Settings = map[string]any{}
		}
		settingsJSON, err := json.Marshal(a.Settings)
		if err != nil {
			return done{}, errors.New("settings could not be read")
		}
		if err := validateSecretInput(schema, settingsJSON); err != nil {
			return done{}, err
		}
		created, err := c.cl.Profile.CreateProfile(c.ctx, connect.NewRequest(&adminv1.CreateProfileRequest{
			Protocol: a.Protocol, Name: a.Name, SettingsJson: string(settingsJSON),
		}))
		if err != nil {
			return done{}, apiError(err)
		}
		profileID := created.Msg.GetProfile().GetId()
		if profileID == "" {
			return done{}, errors.New("the panel created a profile without returning its id")
		}
		var inboundIDs []string
		rollback := func(cause error) error {
			return rollbackCreatedProfile(c, profileID, inboundIDs, cause)
		}
		for _, nodeID := range a.NodeIDs {
			port := a.PortOverrides[nodeID]
			added, err := c.cl.Profile.CreateInbound(c.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
				ProfileId: profileID, NodeId: nodeID, PortOverride: port, AllowLossyPort: a.AllowLossyPort,
			}))
			if err != nil {
				return done{}, rollback(apiError(err))
			}
			inboundIDs = append(inboundIDs, added.Msg.GetInbound().GetId())
		}
		for _, groupID := range a.GroupIDs {
			ids, err := c.groupProfileIDs(groupID)
			if err != nil {
				return done{}, rollback(err)
			}
			// Group membership has no version; write the requested membership over current IDs, like the SPA and twin tool.
			if !slices.Contains(ids, profileID) {
				ids = append(ids, profileID)
			}
			if _, err := c.cl.Group.UpdateGroup(c.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{
				GroupId: groupID, ProfileIds: &adminv1.ProfileIds{Values: ids},
			})); err != nil {
				return done{}, rollback(apiError(err))
			}
		}
		return doneWith("profile_created", "Created the profile and applied all requested placements.",
			"profile_id", profileID, "nodes", strconv.Itoa(len(a.NodeIDs)), "groups", strconv.Itoa(len(a.GroupIDs))), nil
	},
}

func rollbackCreatedProfile(c *call, id string, inboundIDs []string, cause error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(c.ctx), 10*time.Second)
	defer cancel()
	rb := *c
	var failures []string
	for i := len(inboundIDs) - 1; i >= 0; i-- {
		if _, err := rb.cl.Profile.DeleteInbound(ctx, connect.NewRequest(&adminv1.DeleteInboundRequest{InboundId: inboundIDs[i]})); err != nil {
			failures = append(failures, "could not remove an inbound")
		}
	}
	if _, err := rb.cl.Profile.DeleteProfile(ctx, connect.NewRequest(&adminv1.DeleteProfileRequest{ProfileId: id})); err != nil {
		failures = append(failures, "could not remove the new profile")
	}
	if len(failures) == 0 {
		return cause
	}
	prior := outcomeOfError(cause)
	return &outcomeError{msg: cause.Error() + "; rollback incomplete: " + strings.Join(failures, "; "), out: prior}
}

type profileUpdateArgs struct {
	ReasonField
	ProfileID      string         `json:"profile_id" jsonschema:"profile id from profiles_list"`
	Name           *string        `json:"name,omitempty" jsonschema:"optional new profile name"`
	SettingsPatch  map[string]any `json:"settings_patch,omitempty" jsonschema:"optional RFC 7386 JSON merge patch over current settings; omitted x-secret fields are kept; null values are not allowed"`
	AllowLossyPort bool           `json:"allow_lossy_port,omitempty" jsonschema:"save a changed profile port despite a lossy UDP delivery check; this requires owner approval"`
}

type profileUpdateStored struct {
	profileUpdateArgs
	ExpectedVersion uint32 `json:"expected_version"`
}

var profileUpdateProcs = procs(adminv1connect.ProfileServiceGetProfileProcedure, adminv1connect.ProfileServiceListProfilesProcedure,
	adminv1connect.ProfileServiceListProtocolsProcedure, adminv1connect.ProfileServiceUpdateProfileProcedure)

var profileUpdate = changeSpec[profileUpdateArgs]{
	name: "profile_update", min: ProfileAdmin, danger: true, procs: profileUpdateProcs,
	desc: "Update a profile name or settings. The plan shows field changes, the impact estimate, and how many devices need a reissue; every change needs the owner's approval.",
	plan: func(c *call, a profileUpdateArgs) (*planned, error) {
		if err := validID("profile_id", a.ProfileID); err != nil {
			return nil, err
		}
		if a.Name == nil && len(a.SettingsPatch) == 0 {
			return nil, errors.New("give name, settings_patch, or allow_lossy_port")
		}
		if a.Name != nil {
			if err := checkName(*a.Name); err != nil {
				return nil, err
			}
		}
		if a.SettingsPatch != nil && settingsPatchHasNull(a.SettingsPatch) {
			return nil, errors.New("settings_patch must not contain null values")
		}
		current, err := c.cl.Profile.GetProfile(c.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: a.ProfileID}))
		if err != nil {
			return nil, apiError(err)
		}
		profile := current.Msg.GetProfile()
		if profile == nil {
			return nil, errors.New("profile is unavailable")
		}
		if a.Name != nil {
			if err := checkProfileNameAvailable(c, *a.Name, a.ProfileID); err != nil {
				return nil, err
			}
		}
		protocols, err := c.protocols()
		if err != nil {
			return nil, err
		}
		schema, ok := protocols[profile.GetProtocol()]
		if !ok {
			return nil, errors.New("profile protocol is unavailable")
		}
		maskedCurrent, err := maskedSettings(schema, current.Msg.GetSettingsJson())
		if err != nil {
			return nil, errors.New("profile settings are unavailable")
		}
		before, err := decodeSettings(string(maskedCurrent))
		if err != nil {
			return nil, errors.New("profile settings are unavailable")
		}
		var after map[string]any
		if len(a.SettingsPatch) > 0 {
			patchJSON, err := json.Marshal(a.SettingsPatch)
			if err != nil {
				return nil, errors.New("settings_patch could not be read")
			}
			if err := validateSecretInput(schema, patchJSON); err != nil {
				return nil, err
			}
			patch, err := decodeSettings(string(patchJSON))
			if err != nil {
				return nil, err
			}
			merged, ok := mergePatch(before, patch).(map[string]any)
			if !ok {
				return nil, errors.New("settings_patch must be a JSON object")
			}
			after = merged
		} else {
			after = before
		}
		full, err := json.Marshal(after)
		if err != nil {
			return nil, errors.New("settings could not be read")
		}
		version := profile.GetVersion()
		req := &adminv1.UpdateProfileRequest{ProfileId: a.ProfileID, ExpectedVersion: version,
			DryRun: true, AllowLossyPort: a.AllowLossyPort}
		if a.Name != nil {
			req.Name = a.Name
		}
		if len(a.SettingsPatch) > 0 {
			settings := string(full)
			req.SettingsJson = &settings
		}
		dry, err := c.cl.Profile.UpdateProfile(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(req))
		if err != nil {
			return nil, apiError(err)
		}
		changes := make([]Fact, 0)
		if a.Name != nil && profile.GetName() != *a.Name {
			changes = append(changes, profileTextChange("name", profile.GetName(), *a.Name))
		}
		if len(a.SettingsPatch) > 0 {
			changes = append(changes, settingChanges(schema, before, after)...)
		}
		if len(changes) == 0 {
			return nil, errors.New("the profile update does not change any fields")
		}
		impact := dry.Msg.GetImpact()
		if impact == nil {
			impact = &adminv1.ProfileImpact{}
		}
		facts := append(changes,
			Fact{Key: "inbounds_restarted", Value: strconv.FormatUint(uint64(impact.GetInboundsRestarted()), 10)},
			Fact{Key: "users_online", Value: strconv.FormatUint(uint64(impact.GetUsersOnline()), 10)},
			Fact{Key: "devices_need_reissue", Value: strconv.FormatUint(uint64(impact.GetDevicesNeedReissue()), 10)})
		for _, field := range impact.GetCriticalFields() {
			facts = append(facts, Fact{Key: "critical_field", Value: clean(field, 160)})
		}
		return &planned{Summary: "Update one profile using the version shown in the plan.", Facts: facts, Danger: []string{dangerFleet},
			Params: profileUpdateStored{profileUpdateArgs: a, ExpectedVersion: version},
			AuditParams: map[string]any{"profile_id": a.ProfileID, "name": a.Name, "settings_patch": a.SettingsPatch,
				"allow_lossy_port": a.AllowLossyPort}}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var a profileUpdateStored
		if err := strictJSON([]byte(pl.ParamsJSON), &a); err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		current, err := c.cl.Profile.GetProfile(c.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: a.ProfileID}))
		if err != nil {
			if isNotFound(err) {
				return done{}, failure("changed_since_plan", "the profile no longer exists; make a new plan")
			}
			return done{}, apiError(err)
		}
		profile := current.Msg.GetProfile()
		if profile == nil || profile.GetVersion() != a.ExpectedVersion {
			return done{}, failure("changed_since_plan", "the profile version changed after the plan; make a new plan")
		}
		protocols, err := c.protocols()
		if err != nil {
			return done{}, err
		}
		schema, ok := protocols[profile.GetProtocol()]
		if !ok {
			return done{}, errors.New("profile protocol is unavailable")
		}
		req := &adminv1.UpdateProfileRequest{ProfileId: a.ProfileID, ExpectedVersion: a.ExpectedVersion, AllowLossyPort: a.AllowLossyPort}
		if a.Name != nil {
			req.Name = a.Name
		}
		if len(a.SettingsPatch) > 0 {
			if settingsPatchHasNull(a.SettingsPatch) {
				return done{}, errors.New("settings_patch must not contain null values")
			}
			patchJSON, err := json.Marshal(a.SettingsPatch)
			if err != nil {
				return done{}, errors.New("settings_patch could not be read")
			}
			if err := validateSecretInput(schema, patchJSON); err != nil {
				return done{}, err
			}
			masked, err := maskedSettings(schema, current.Msg.GetSettingsJson())
			if err != nil {
				return done{}, errors.New("profile settings are unavailable")
			}
			before, err := decodeSettings(string(masked))
			if err != nil {
				return done{}, errors.New("profile settings are unavailable")
			}
			patch, err := decodeSettings(string(patchJSON))
			if err != nil {
				return done{}, errors.New("settings_patch could not be read")
			}
			after, ok := mergePatch(before, patch).(map[string]any)
			if !ok {
				return done{}, errors.New("settings_patch must be a JSON object")
			}
			settingsJSON, err := json.Marshal(after)
			if err != nil {
				return done{}, errors.New("settings could not be read")
			}
			if err := validateSecretInput(schema, settingsJSON); err != nil {
				return done{}, err
			}
			settings := string(settingsJSON)
			req.SettingsJson = &settings
		}
		r, err := c.cl.Profile.UpdateProfile(c.ctx, connect.NewRequest(req))
		if err != nil {
			var ce *connect.Error
			if errors.As(err, &ce) && ce.Code() == connect.CodeAborted {
				return done{}, failure("changed_since_plan", "the profile version changed after the plan; make a new plan")
			}
			return done{}, apiError(err)
		}
		return doneWith("profile_updated", "Updated the profile.", "profile_id", r.Msg.GetProfile().GetId(),
			"version", strconv.FormatUint(uint64(r.Msg.GetProfile().GetVersion()), 10)), nil
	},
}

func profileTextChange(key, before, after string) Fact {
	f := changeFact(key, strconv.Quote(clean(before, 160))+" -> "+strconv.Quote(clean(after, 160)), clean(before, 160), clean(after, 160))
	f.Untrusted = true
	f.UntrustedParams = []string{"from", "to"}
	return f
}

func settingChanges(schema map[string]any, before, after map[string]any) []Fact {
	var out []Fact
	var walk func(map[string]any, map[string]any, map[string]any, string)
	walk = func(s map[string]any, old, next map[string]any, prefix string) {
		props, _ := s["properties"].(map[string]any)
		keys := map[string]bool{}
		for k := range old {
			keys[k] = true
		}
		for k := range next {
			keys[k] = true
		}
		order := make([]string, 0, len(keys))
		for k := range keys {
			order = append(order, k)
		}
		sort.Strings(order)
		for _, key := range order {
			oldValue, oldOK := old[key]
			newValue, newOK := next[key]
			if oldOK == newOK && reflect.DeepEqual(oldValue, newValue) {
				continue
			}
			child, _ := props[key].(map[string]any)
			path := prefix + "/" + escapeJSONPointer(key)
			if child["x-secret"] == true {
				if newValue == "$generate" || oldValue == profileSecretMask && newValue != profileSecretMask {
					out = append(out, Fact{Key: "setting:" + path, Value: "masked secret -> generated secret"})
				} else {
					out = append(out, Fact{Key: "setting:" + path, Value: "secret field changed"})
				}
				continue
			}
			if oldObj, ok := oldValue.(map[string]any); ok {
				if newObj, ok := newValue.(map[string]any); ok {
					walk(child, oldObj, newObj, path)
					continue
				}
			}
			from, to := settingValue(oldValue, oldOK), settingValue(newValue, newOK)
			f := changeFact("setting:"+path, from+" -> "+to, from, to)
			f.Untrusted = true
			f.UntrustedParams = []string{"from", "to"}
			out = append(out, f)
		}
	}
	walk(schema, before, after, "")
	return out
}

func escapeJSONPointer(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func settingValue(v any, present bool) string {
	if !present {
		return "<missing>"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "<unavailable>"
	}
	return clean(string(b), 180)
}

type profileDeployArgs struct {
	ReasonField
	ProfileID      string            `json:"profile_id" jsonschema:"profile id from profiles_list"`
	AddNodeIDs     []string          `json:"add_node_ids,omitempty" jsonschema:"nodes to add the profile to"`
	PortOverrides  map[string]uint32 `json:"port_overrides,omitempty" jsonschema:"node id to port override for added nodes"`
	AllowLossyPort bool              `json:"allow_lossy_port,omitempty" jsonschema:"accept a lossy UDP port check for added nodes; this requires owner approval"`
	RemoveNodeIDs  []string          `json:"remove_node_ids,omitempty" jsonschema:"nodes to remove the profile from"`
	AddGroupIDs    []string          `json:"add_group_ids,omitempty" jsonschema:"groups that should receive the profile"`
	RemoveGroupIDs []string          `json:"remove_group_ids,omitempty" jsonschema:"groups that should lose the profile"`
}

type profileDeployStored struct {
	profileDeployArgs
	ExpectedVersion uint32   `json:"expected_version"`
	RemovalIDs      []string `json:"removal_ids,omitempty"`
}

var profileDeployProcs = procs(adminv1connect.ProfileServiceGetProfileProcedure, adminv1connect.ProfileServiceCreateInboundProcedure,
	adminv1connect.ProfileServiceDeleteInboundProcedure, adminv1connect.GroupServiceListGroupsProcedure,
	adminv1connect.GroupServiceUpdateGroupProcedure, adminv1connect.NodeServiceListNodesProcedure)

var profileDeploy = changeSpec[profileDeployArgs]{
	name: "profile_deploy", min: ProfileAdmin, danger: true, procs: profileDeployProcs,
	desc: "Add or remove a profile on nodes and in groups. The plan checks additions and counts users affected by removals; every change needs the owner's approval.",
	plan: func(c *call, a profileDeployArgs) (*planned, error) {
		if err := validID("profile_id", a.ProfileID); err != nil {
			return nil, err
		}
		adds, err := uniqueIDs("add_node_ids", a.AddNodeIDs, true)
		if err != nil {
			return nil, err
		}
		removes, err := uniqueIDs("remove_node_ids", a.RemoveNodeIDs, true)
		if err != nil {
			return nil, err
		}
		addGroups, err := uniqueIDs("add_group_ids", a.AddGroupIDs, true)
		if err != nil {
			return nil, err
		}
		removeGroups, err := uniqueIDs("remove_group_ids", a.RemoveGroupIDs, true)
		if err != nil {
			return nil, err
		}
		if len(adds)+len(removes)+len(addGroups)+len(removeGroups) == 0 {
			return nil, errors.New("give at least one node or group add/remove list")
		}
		if a.AllowLossyPort && len(adds) == 0 {
			return nil, errors.New("allow_lossy_port requires add_node_ids")
		}
		for _, id := range adds {
			if slices.Contains(removes, id) {
				return nil, errors.New("a node cannot be added and removed in the same plan")
			}
		}
		for _, id := range addGroups {
			if slices.Contains(removeGroups, id) {
				return nil, errors.New("a group cannot be added and removed in the same plan")
			}
		}
		portOverrides, err := profileCreatePortOverrides(adds, a.PortOverrides)
		if err != nil {
			return nil, err
		}
		got, err := c.cl.Profile.GetProfile(c.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: a.ProfileID}))
		if err != nil {
			return nil, apiError(err)
		}
		profile := got.Msg.GetProfile()
		if profile == nil {
			return nil, errors.New("profile is unavailable")
		}
		current := got.Msg.GetInbounds()
		byNode := map[string]*adminv1.Inbound{}
		for _, in := range current {
			byNode[in.GetNodeId()] = in
		}
		nodeList, err := c.cl.Node.ListNodes(c.ctx, connect.NewRequest(&adminv1.ListNodesRequest{}))
		if err != nil {
			return nil, apiError(err)
		}
		nodeNames := map[string]string{}
		for _, n := range nodeList.Msg.GetNodes() {
			nodeNames[n.GetId()] = n.GetName()
		}
		groupList, err := c.getGroups()
		if err != nil {
			return nil, err
		}
		addSnaps, err := groupSnapshots(groupList, addGroups)
		if err != nil {
			return nil, err
		}
		removeSnaps, err := groupSnapshots(groupList, removeGroups)
		if err != nil {
			return nil, err
		}
		for _, id := range adds {
			if nodeNames[id] == "" {
				return nil, errors.New("node not found")
			}
			if byNode[id] != nil {
				return nil, errors.New("profile is already on a selected node")
			}
		}
		for _, id := range removes {
			if byNode[id] == nil {
				return nil, errors.New("profile is not on a selected node")
			}
		}
		for _, snap := range addSnaps {
			if g := findGroup(groupList, snap.ID); slices.Contains(g.GetProfileIds(), a.ProfileID) {
				return nil, errors.New("profile is already in a selected group")
			}
		}
		groupLoss := map[string]uint32{}
		for _, snap := range removeSnaps {
			g := findGroup(groupList, snap.ID)
			if !slices.Contains(g.GetProfileIds(), a.ProfileID) {
				return nil, errors.New("profile is not in a selected group")
			}
			ids := slices.DeleteFunc(slices.Clone(g.GetProfileIds()), func(id string) bool { return id == a.ProfileID })
			dry, err := c.cl.Group.UpdateGroup(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.UpdateGroupRequest{
				GroupId: snap.ID, ProfileIds: &adminv1.ProfileIds{Values: ids}, DryRun: true,
			}))
			if err != nil {
				return nil, apiError(err)
			}
			if hasImpactProfile(dry.Msg.GetImpact().GetLost(), a.ProfileID) {
				groupLoss[snap.ID] = dry.Msg.GetImpact().GetUsers()
			} else {
				groupLoss[snap.ID] = 0
			}
		}
		facts := []Fact{{Key: "user_count", Value: strconv.FormatUint(uint64(profile.GetUserCount()), 10)}}
		for _, id := range adds {
			validated, err := c.cl.Profile.CreateInbound(c.e.cfg.Auth.WithPlanning(c.ctx), connect.NewRequest(&adminv1.CreateInboundRequest{
				ProfileId: a.ProfileID, NodeId: id, PortOverride: portOverrides[id], ValidateOnly: true, AllowLossyPort: a.AllowLossyPort,
			}))
			if err != nil {
				return nil, apiError(err)
			}
			warnings := profileWarnings(validated.Msg.GetWarnings())
			facts = append(facts, Fact{Key: "add_node", Value: nm(nodeNames[id]), Untrusted: true},
				Fact{Key: "port", Value: strconv.FormatUint(uint64(validated.Msg.GetInbound().GetPort()), 10)},
				Fact{Key: "warnings", Value: warningText(warnings)})
		}
		removalIDs := make([]string, 0, len(removes))
		for _, id := range removes {
			in := byNode[id]
			removalIDs = append(removalIDs, in.GetId())
			facts = append(facts, Fact{Key: "remove_node", Value: nm(in.GetNodeName()), Untrusted: true})
		}
		for _, snap := range addSnaps {
			facts = append(facts, Fact{Key: "add_group", Value: snap.Name, Untrusted: true}, Fact{Key: "users", Value: strconv.FormatUint(uint64(snap.UserCount), 10)})
		}
		for _, snap := range removeSnaps {
			users := groupLoss[snap.ID]
			facts = append(facts, Fact{Key: "remove_group", Value: snap.Name, Untrusted: true}, Fact{Key: "users_losing_server", Value: strconv.FormatUint(uint64(users), 10)})
		}
		a.AddNodeIDs, a.RemoveNodeIDs, a.AddGroupIDs, a.RemoveGroupIDs = adds, removes, addGroups, removeGroups
		a.PortOverrides = portOverrides
		stored := profileDeployStored{profileDeployArgs: a, ExpectedVersion: profile.GetVersion(), RemovalIDs: removalIDs}
		return &planned{Summary: "Change the node and group placements of one profile.", Facts: facts, Danger: []string{dangerFleet}, Params: stored}, nil
	},
	applyStored: func(c *call, pl Plan) (done, error) {
		var a profileDeployStored
		if err := strictJSON([]byte(pl.ParamsJSON), &a); err != nil {
			return done{}, failure("plan_unreadable", "the stored plan is unreadable: make a new plan")
		}
		progress := profileDeployProgress{}
		fail := func(step string, err error) (done, error) { return done{}, profileDeployFailure(step, err, progress) }
		if len(a.RemovalIDs) != len(a.RemoveNodeIDs) {
			return fail("remove_node", failure("plan_unreadable", "the stored plan is unreadable: make a new plan"))
		}
		for _, nodeID := range a.AddNodeIDs {
			if err := c.profileVersionMatches(a.ProfileID, a.ExpectedVersion); err != nil {
				return fail("add_node", err)
			}
			_, err := c.cl.Profile.CreateInbound(c.ctx, connect.NewRequest(&adminv1.CreateInboundRequest{
				ProfileId: a.ProfileID, NodeId: nodeID, PortOverride: a.PortOverrides[nodeID], AllowLossyPort: a.AllowLossyPort,
			}))
			if err != nil {
				if isNotFound(err) {
					return fail("add_node", failure("changed_since_plan", "a selected node no longer exists; make a new plan"))
				}
				return fail("add_node", apiError(err))
			}
			progress.AddedNodes++
		}
		for _, groupID := range a.AddGroupIDs {
			ids, err := c.groupProfileIDs(groupID)
			if err != nil {
				return fail("add_group", err)
			}
			// Groups have no version; keep current links and apply this membership last-write-wins, like the SPA and twin tool.
			if !slices.Contains(ids, a.ProfileID) {
				ids = append(ids, a.ProfileID)
				if _, err := c.cl.Group.UpdateGroup(c.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{GroupId: groupID, ProfileIds: &adminv1.ProfileIds{Values: ids}})); err != nil {
					if isNotFound(err) {
						return fail("add_group", failure("changed_since_plan", "a selected group no longer exists; make a new plan"))
					}
					return fail("add_group", apiError(err))
				}
			}
			progress.AddedGroups++
		}
		for i := range a.RemoveNodeIDs {
			if err := c.profileVersionMatches(a.ProfileID, a.ExpectedVersion); err != nil {
				return fail("remove_node", err)
			}
			if _, err := c.cl.Profile.DeleteInbound(c.ctx, connect.NewRequest(&adminv1.DeleteInboundRequest{InboundId: a.RemovalIDs[i]})); err != nil {
				if isNotFound(err) {
					return fail("remove_node", failure("changed_since_plan", "the selected node or inbound no longer exists; make a new plan"))
				}
				return fail("remove_node", apiError(err))
			}
			progress.RemovedNodes++
		}
		for _, groupID := range a.RemoveGroupIDs {
			ids, err := c.groupProfileIDs(groupID)
			if err != nil {
				return fail("remove_group", err)
			}
			// Groups have no version; keep current links and apply this membership last-write-wins, like the SPA and twin tool.
			if slices.Contains(ids, a.ProfileID) {
				ids = slices.DeleteFunc(ids, func(id string) bool { return id == a.ProfileID })
				if _, err := c.cl.Group.UpdateGroup(c.ctx, connect.NewRequest(&adminv1.UpdateGroupRequest{GroupId: groupID, ProfileIds: &adminv1.ProfileIds{Values: ids}})); err != nil {
					if isNotFound(err) {
						return fail("remove_group", failure("changed_since_plan", "a selected group no longer exists; make a new plan"))
					}
					return fail("remove_group", apiError(err))
				}
			}
			progress.RemovedGroups++
		}
		return doneWith("profile_deployed", "Applied the requested profile placements.", "profile_id", a.ProfileID,
			"added_nodes", strconv.Itoa(progress.AddedNodes), "removed_nodes", strconv.Itoa(progress.RemovedNodes),
			"added_groups", strconv.Itoa(progress.AddedGroups), "removed_groups", strconv.Itoa(progress.RemovedGroups)), nil
	},
}

func (c *call) profileVersionMatches(id string, version uint32) error {
	r, err := c.cl.Profile.GetProfile(c.ctx, connect.NewRequest(&adminv1.GetProfileRequest{ProfileId: id}))
	if err != nil {
		if isNotFound(err) {
			return failure("changed_since_plan", "the profile no longer exists; make a new plan")
		}
		return apiError(err)
	}
	if r.Msg.GetProfile().GetVersion() != version {
		return failure("changed_since_plan", "the profile changed since the plan; make a new plan")
	}
	return nil
}

type profileDeployProgress struct {
	AddedNodes, RemovedNodes, AddedGroups, RemovedGroups int
}

func (p profileDeployProgress) completed() int {
	return p.AddedNodes + p.RemovedNodes + p.AddedGroups + p.RemovedGroups
}

func profileDeployFailure(step string, err error, completed profileDeployProgress) error {
	out := outcomeOfError(err)
	if out.Code == "" {
		out.Code = "profile_deploy_failed"
	}
	out.Params = map[string]string{"step": step, "completed_steps": strconv.Itoa(completed.completed())}
	msg := "Profile deployment failed during " + step + " (panel code " + out.Code + ") after " + strconv.Itoa(completed.completed()) + " completed steps."
	if out.Code == "changed_since_plan" {
		msg = "Profile deployment stopped during " + step + " (panel code " + out.Code + ") because a planned target changed or disappeared after " + strconv.Itoa(completed.completed()) + " completed steps."
	}
	return &outcomeError{msg: msg, out: out}
}

func hasImpactProfile(rows []*adminv1.ImpactProfile, id string) bool {
	for _, p := range rows {
		if p.GetId() == id {
			return true
		}
	}
	return false
}
