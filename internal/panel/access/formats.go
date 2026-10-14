package access

import (
	"encoding/json"
	"strings"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/protocols"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/plugin"
)

// The trial render of a profile: which subscription formats carry it, answered by the same Render the subscription calls,
// on a masked, made-up inbound. Nothing but the ok flag and the note code leaves it; the fragment's data is dropped.

// profileFormatOrder is the order of ProfileSummary.formats: link list, Mihomo, Xray JSON, sing-box JSON.
var profileFormatOrder = []struct {
	render plugin.ClientFormat
	api    adminv1.SubFormat
}{
	{plugin.FormatURIList, adminv1.SubFormat_SUB_FORMAT_BASE64_URIS},
	{plugin.FormatMihomo, adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML},
	{plugin.FormatXrayJSON, adminv1.SubFormat_SUB_FORMAT_XRAY_JSON},
	{plugin.FormatSingbox, adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON},
}

// trialNode is the node a trial render uses when the caller names none.
var trialNode = store.AccessNode{Name: "node", Address: "example.com"}

// trialPin stands for the certificate pin a self-signed node reports after it applied the inbound. The subscription
// leaves a self-signed server out until the pin is known; a profile is judged as it will be once it is, so the format
// answers are those of a working node.
var trialPin = strings.Repeat("ab", 32)

// trialInbound is the inbound a profile's trial render is built on: InitInbound's throwaway key (never shown) so that
// the inbound can be built at all.
func (s *Service) trialInbound(proto protocols.Protocol, merged json.RawMessage, node store.AccessNode) (protocols.InboundInput, error) {
	in := protocols.InboundInput{Profile: protocols.ProfileView{Settings: merged}, Node: nodeView(node), Enabled: true, SpecVersion: 1}
	if ini, ok := proto.(protocols.InboundInitializer); ok {
		made, err := ini.InitInbound(protocols.InboundInitInput{Profile: in.Profile, Node: in.Node})
		if err != nil {
			return in, err
		}
		in.PluginState, in.PluginPublic = made.State, made.Public
	}
	return in, nil
}

// trialRender is one masked render of a profile on the trial inbound, as the subscription would make it.
func trialRender(proto protocols.Protocol, merged json.RawMessage, node store.AccessNode, in protocols.InboundInput, spec plugin.InboundSpec, format plugin.ClientFormat, pin string) (plugin.Fragment, bool) {
	return proto.Render(protocols.RenderInput{
		Format: format, Settings: merged, UserName: "user", MaskSecrets: true,
		Inbound: inboundView(nodeView(node), spec, pin), InboundPublic: in.PluginPublic, NodeAddr: node.Address,
	})
}

// trialFormats says, for each subscription format, whether the profile is rendered in it and why not (or with what loss).
func trialFormats(proto protocols.Protocol, merged json.RawMessage, node store.AccessNode, in protocols.InboundInput, spec plugin.InboundSpec) []*adminv1.ProfileFormat {
	pin := ""
	if spec.TLS.Mode == plugin.TLSSelfSigned {
		pin = trialPin
	}
	out := make([]*adminv1.ProfileFormat, 0, len(profileFormatOrder))
	for _, f := range profileFormatOrder {
		frag, ok := trialRender(proto, merged, node, in, spec, f.render, pin)
		note := frag.Note
		if !ok && note == "" {
			note = "unsupported"
		}
		out = append(out, &adminv1.ProfileFormat{Format: f.api, Ok: ok, Note: note})
	}
	return out
}

// profileFormats is trialFormats for a stored profile on the placeholder node. A profile that cannot be built (it would
// not deploy either) has no answer: nil.
func (s *Service) profileFormats(proto protocols.Protocol, merged json.RawMessage) []*adminv1.ProfileFormat {
	in, err := s.trialInbound(proto, merged, trialNode)
	if err != nil {
		return nil
	}
	spec, err := proto.BuildInbound(in)
	if err != nil {
		return nil
	}
	return trialFormats(proto, merged, trialNode, in, spec)
}
