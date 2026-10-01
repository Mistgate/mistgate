//go:build linux

package awgnl

import (
	"errors"
	"os"
	"testing"

	"github.com/mdlayher/genetlink"
	"github.com/mdlayher/genetlink/genltest"
	"github.com/mdlayher/netlink"
)

// serve answers the two controller requests probe makes: the family (id, version, groups) and its attributes (maxattr).
func serve(version uint8, maxattr uint32) genltest.Func {
	return func(greq genetlink.Message, nreq netlink.Message) ([]genetlink.Message, error) {
		ae := netlink.NewAttributeEncoder()
		ae.Uint16(1, famID) // CTRL_ATTR_FAMILY_ID
		ae.String(2, "amneziawg")
		ae.Uint32(3, uint32(version))
		ae.Uint32(ctrlAttrMaxAttr, maxattr)
		ae.Nested(7, func(g *netlink.AttributeEncoder) error { // CTRL_ATTR_MCAST_GROUPS
			g.Nested(0, func(x *netlink.AttributeEncoder) error {
				x.String(1, "auth")
				x.Uint32(2, 9)
				return nil
			})
			return nil
		})
		b, err := ae.Encode()
		return []genetlink.Message{{Header: genetlink.Header{Command: 1}, Data: b}}, err
	}
}

func TestProbe(t *testing.T) {
	cl, err := probe(genltest.Dial(serve(3, 34)))
	if err != nil {
		t.Fatal(err)
	}
	if cl.Info != (Info{ID: famID, GenlVersion: 3, MaxAttr: 34}) || !cl.Info.Is31() {
		t.Errorf("info %+v", cl.Info)
	}
	if len(cl.family.Groups) != 1 || cl.family.Groups[0].Name != "auth" {
		t.Errorf("groups %+v", cl.family.Groups)
	}
	cl, err = probe(genltest.Dial(serve(3, 32)))
	if err != nil || cl.Info.Is31() {
		t.Errorf("a maxattr 32 module is AWG 3.0, not 3.1: %+v %v", cl, err)
	}
	if _, err := probe(genltest.Dial(serve(2, 30))); err == nil {
		t.Error("genl version 2 (AWG 2.0 module) must be refused: a different encoding")
	}
}

func TestProbeNoModule(t *testing.T) {
	c := genltest.Dial(func(genetlink.Message, netlink.Message) ([]genetlink.Message, error) {
		return nil, genltest.Error(int(2)) // ENOENT: the family does not exist
	})
	if _, err := probe(c); !errors.Is(err, ErrNoModule) {
		t.Fatalf("want ErrNoModule, got %v (os.ErrNotExist=%v)", err, os.ErrNotExist)
	}
}
