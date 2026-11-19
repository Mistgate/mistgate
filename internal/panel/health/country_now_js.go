//go:build js

package health

import (
	"context"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

// The Cloudflare edition has no client-eye checks and no background work: "Check now" asks the country check for nothing
// (country_now.go belongs to the VPS edition).
func (s *Service) RunCountryNow(context.Context, string) *adminv1.CountryNow { return nil }

func (s *Service) countryNowViews(context.Context, string) []*adminv1.CountryNow { return nil }
