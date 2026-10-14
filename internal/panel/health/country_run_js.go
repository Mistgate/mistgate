//go:build js

package health

import "context"

// On the Cloudflare edition the cron calls CountryTick (app.EdgeTick); there is nothing to run in the background.
func (s *Service) runCountryChecks(ctx context.Context) {
	<-ctx.Done()
}
