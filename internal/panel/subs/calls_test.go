package subs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/internal/panel/access"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// fakeDevs is a source that counts the views built for a token and manages devices: every device call is "not found".
type fakeDevs struct{ *fakeSrc }

var errNoDevice = connect.NewError(connect.CodeNotFound, nil)

func (f fakeDevs) SubscriptionWith(ctx context.Context, token string, _ access.SubOptions) (access.SubView, error) {
	return f.Subscription(ctx, token)
}
func (fakeDevs) AddAWGDevice(context.Context, string, string, string, string, string) (store.AccessAWGDevice, []access.DeviceConfig, error) {
	return store.AccessAWGDevice{}, nil, errNoDevice
}
func (fakeDevs) DeviceConfigs(context.Context, string, string, string) (store.AccessAWGDevice, []access.DeviceConfig, error) {
	return store.AccessAWGDevice{}, nil, errNoDevice
}
func (fakeDevs) RotateDevice(context.Context, string, string, string) (store.AccessAWGDevice, []access.DeviceConfig, error) {
	return store.AccessAWGDevice{}, nil, errNoDevice
}
func (fakeDevs) RelabelDevice(context.Context, string, string, string) (store.AccessAWGDevice, error) {
	return store.AccessAWGDevice{}, errNoDevice
}
func (fakeDevs) RevokeOwnDevice(context.Context, string, string, string) error { return errNoDevice }

// A call that is refused for reasons the token's state already tells (the hourly budget of writes, another origin) is
// answered before the view of the person is built: the view is the expensive part, and a link that is over its budget must not
// cost one per request. An unknown token still gets the decoy first, whatever else is wrong with the request.
func TestRefusedCallsDoNotBuildTheView(t *testing.T) {
	src := &fakeSrc{valid: map[string]access.SubView{tokA: {UserName: "alice", Status: access.StatusActive}}}
	h := Handler(fakeDevs{src}, decoyHandler, Config{MaxWritesPerHour: 2, MinInterval: -1, MaxPerHour: -1})
	post := func(token string, hdr ...string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/"+token+"/devices/dev_abcde/rename", strings.NewReader(`{"label":"x"}`))
		req.Header.Set("Content-Type", "application/json")
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	cross := []string{"Sec-Fetch-Site", "cross-site"}

	// Not yet known to the handler: the decoy, though the request is cross-origin as well.
	if rec := post(unknownToken(1), cross...); !isDecoy(rec) {
		t.Fatalf("an unknown token: %d %s", rec.Code, rec.Body)
	}
	for i := 0; i < 2; i++ { // the budget: two writes, each of them a view
		if rec := post(tokA); rec.Code != http.StatusNotFound {
			t.Fatalf("write %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	built := src.lookups()
	if rec := post(tokA); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over the budget: %d %s", rec.Code, rec.Body)
	}
	if rec := post(tokA, cross...); rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "cross_origin") {
		t.Fatalf("another origin: %d %s", rec.Code, rec.Body)
	}
	if got := src.lookups(); got != built {
		t.Errorf("refused calls built %d views", got-built)
	}
	if rec := post(unknownToken(2), cross...); !isDecoy(rec) {
		t.Errorf("an unknown token, still: %d %s", rec.Code, rec.Body)
	}
}
