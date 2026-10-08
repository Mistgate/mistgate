//go:build js

package health

import "context"

func (s *Service) runPortRechecks(ctx context.Context) {
	<-ctx.Done()
}
