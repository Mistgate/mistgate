package subsettings

import (
	"context"
	"testing"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestLinkAutoSelectAbsentMeansOnAndSurvivesSettingsRoundTrip(t *testing.T) {
	if !LinkAutoSelect(nil) || !LinkAutoSelect(&adminv1.SubscriptionSettings{}) || !LinkAutoSelect(Defaults()) || Defaults().LinkAutoSelect != nil {
		t.Fatal("an absent link_auto_select setting must be on and Defaults must leave it unset")
	}
	old, err := Parse(`{"title":"old settings"}`)
	if err != nil || !LinkAutoSelect(old) || old.LinkAutoSelect != nil {
		t.Fatalf("old stored settings = %v, %v; want absent and on", old, err)
	}

	ctx := context.Background()
	st := openStore(t)
	cache := NewCacheWithoutMigrations(st, nil)
	for _, want := range []bool{true, false} {
		settings := Defaults()
		settings.LinkAutoSelect = proto.Bool(want)
		updated, err := cache.Update(ctx, settings)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := Load(ctx, st)
		if err != nil || loaded.LinkAutoSelect == nil || LinkAutoSelect(updated) != want || LinkAutoSelect(loaded) != want {
			t.Fatalf("link_auto_select round trip = updated:%v loaded:%v err:%v; want %t", updated.GetLinkAutoSelect(), loaded.GetLinkAutoSelect(), err, want)
		}
	}
}
