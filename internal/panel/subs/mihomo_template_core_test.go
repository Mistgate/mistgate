package subs

import (
	"strings"
	"testing"
)

// The Mihomo core judges the profiles built around templates, as it does the built-in ones (see mihomo_core_test.go for the binary):
// the result of each template that the repository holds, under the shapes of preset and switch that change what the panel adds.
func TestMihomoCoreAcceptsTemplates(t *testing.T) {
	mihomoBin(t)
	router, pc := templateText(t, "mihomo_template_router.yaml"), templateText(t, "mihomo_template_pc.yaml")
	for _, c := range []struct {
		name string
		tpl  string
		mut  func(*mihomoProfile)
	}{
		{"router with the dns of the preset and Auto", router, nil},
		{"router with no preset and no dns", router, func(p *mihomoProfile) { p.preset = nil }},
		{"router and a preset with country lists", router, func(p *mihomoProfile) { p.preset = cnSplit() }},
		{"router with one server", router, func(p *mihomoProfile) { p.lines, p.names = p.lines[:1], p.names[:1] }},
		{"router without Auto", strings.Replace(router, "proxies: [Auto, DIRECT] # the panel's Auto group", "proxies: [DIRECT]", 1), func(p *mihomoProfile) { p.autoSelect = false }},
		{"pc with its own dns and a filter that matches nothing", pc, nil},
		{"a group of only the servers", "proxy-groups:\n  - name: Proxy\n    type: select\n    include-all-proxies: true\nrules:\n  - MATCH,Proxy\n", nil},
		{"a template of one key", "mixed-port: 7890\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := twoServers(t)
			if c.mut != nil {
				c.mut(&p)
			}
			out, used := templated(t, c.tpl, p)
			if !used {
				t.Fatal("the built-in profile was sent")
			}
			MihomoCheck(t, []byte(out))
		})
	}
}
