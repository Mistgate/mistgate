package store

import (
	"context"
	"time"
)

// The tests of the shared device were written against two calls that EnsureLinkDevice and EnsureLinkAWGCreds replaced.
// They stay as the way these tests make a shared device with the credentials they hand over.

// EnsureImplicitDevice makes the user's shared device hold creds (the ones it has no protocol for yet) and reads it back;
// created reports credentials added. No credentials and no device: nothing is written.
func (a Access) EnsureImplicitDevice(ctx context.Context, dev AccessDevice, creds []AccessCred) (AccessDevice, []AccessCred, bool, error) {
	dev.Implicit, dev.HWIDHash = true, nil
	res, err := a.EnsureLinkDevice(ctx, dev, 0, dev.CreatedAt, func(deviceID string, have map[string]bool) ([]AccessCred, error) {
		var out []AccessCred
		for _, c := range creds {
			if !have[c.Protocol] {
				out = append(out, c)
			}
		}
		return out, nil
	})
	return res.Device, res.Creds, len(res.Added) > 0, err
}

// EnsureImplicitAWGCreds is EnsureLinkAWGCreds on the shared device of userID.
func (a Access) EnsureImplicitAWGCreds(ctx context.Context, userID string, dev AccessDevice, now time.Time, want []AWGImplicitWant) (LinkDeviceResult, error) {
	dev.UserID, dev.Implicit, dev.HWIDHash = userID, true, nil
	return a.EnsureLinkAWGCreds(ctx, dev, 0, now, want)
}
