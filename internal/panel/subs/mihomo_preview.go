package subs

import (
	"context"
	"errors"
	"slices"
	"time"

	"connectrpc.com/connect"
	"gopkg.in/yaml.v3"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
	"github.com/mistgate/mistgate/internal/plugin"
)

// PreviewMihomoTemplate builds the Mihomo profile one person would get from the editor's draft of a template, the way the answer of
// their link builds it, and says what the owner should look at. It reads the person's view without writing (no device, no
// credential), and the servers of the person come out with their name, type, address and port only: the owner sees where the
// servers went in the template, not the keys of the person.
func (s *Service) PreviewMihomoTemplate(ctx context.Context, req *connect.Request[adminv1.PreviewMihomoTemplateRequest]) (*connect.Response[adminv1.PreviewMihomoTemplateResponse], error) {
	m := req.Msg
	set, err := subsettings.Load(ctx, s.st) // not the cache: the draft is checked against what is saved
	if err != nil {
		return nil, s.internal("preview load", err)
	}
	autoOn := subsettings.LinkAutoSelect(set)
	if err := subsettings.CheckMihomoGroups(m.Yaml, m.GroupIds, autoOn); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if s.preview == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, errors.New("the preview is not available"))
	}
	v, err := s.preview.PreviewSubscriptionWith(ctx, m.UserId, access.SubOptions{
		Format: plugin.FormatMihomo, NoLinkAWG: !subsettings.LinkAWG(set), IncludeEffectiveDNS: true,
	})
	if errors.Is(err, access.ErrUnknownToken) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("no such person"))
	} else if err != nil {
		return nil, s.internal("preview view", err)
	}
	var lang string
	if b, err := s.brand(ctx); err == nil {
		lang = b.Language
	}
	p := mihomoProfileOf(v, set, s.effectiveTitle(ctx, set), lang, time.Now(), s.log)
	if !slices.Contains(m.GroupIds, v.GroupID) {
		p.groupID = ""
	}
	eff, _ := s.dns.(Effective)
	p.preset = linkPresetOf(ctx, v, eff, s.log)
	if !p.note {
		p.lines = redactedLines(p.lines)
	}
	body, selected, templated, err := p.render("draft", m.Yaml)
	if err != nil {
		return nil, s.internal("preview render", err)
	}
	if p.note {
		// The profile ignores templates when serving a note, but the editor still needs the variant's warnings.
		selected, err = subsettings.ResolveMihomoGroup(m.Yaml, p.groupID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	resp := &adminv1.PreviewMihomoTemplateResponse{Yaml: string(body)}
	if g, err := s.st.Access().Group(ctx, v.GroupID); err == nil {
		resp.GroupName = g.Name
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, s.internal("preview group", err)
	}
	if !templated {
		resp.NotServed = "no_servers"
		if v.Status != access.StatusActive {
			resp.NotServed = "no_access"
		}
	}
	for _, w := range subsettings.MihomoTemplateWarnings(selected, autoOn) {
		resp.Warnings = append(resp.Warnings, &adminv1.MihomoWarning{Code: w.Code, Arg: w.Arg})
	}
	if p.preset != nil {
		if d, err := p.preset.MihomoDNS(""); err == nil && len(d.Direct)+len(d.Providers)+len(d.Block)+len(d.BlockDomains)+len(d.Torrent) > 0 {
			resp.Warnings = append(resp.Warnings, &adminv1.MihomoWarning{Code: subsettings.WarnPresetRulesSkipped})
		}
	}
	if len(m.GroupIds) == 0 {
		resp.Warnings = append(resp.Warnings, &adminv1.MihomoWarning{Code: subsettings.WarnUnused})
	}
	return connect.NewResponse(resp), nil
}

// redactedKeys are what a server keeps in the preview.
var redactedKeys = []string{"name", "type", "server", "port"}

// redactedLines keeps of each proxy only its name, type, address and port. The lines stay where they were (the names are parallel
// to them): one that does not parse is left as it is, and the profile skips it.
func redactedLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		m, _, ok := proxyNode(line)
		if !ok {
			out = append(out, line)
			continue
		}
		kept := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for i := 0; i+1 < len(m.Content); i += 2 {
			for _, k := range redactedKeys {
				if m.Content[i].Value == k {
					kept.Content = append(kept.Content, m.Content[i], m.Content[i+1])
				}
			}
		}
		b, err := yaml.Marshal(&yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{kept}})
		if err != nil {
			out = append(out, line)
			continue
		}
		out = append(out, string(b))
	}
	return out
}
