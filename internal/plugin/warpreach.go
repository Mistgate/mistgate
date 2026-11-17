//go:build !js

package plugin

import "regexp"

// MaxWarpReachDomains is the most sites one WARP reach run takes (agent.proto "WARP REACH").
const MaxWarpReachDomains = 8

var warpReachDomain = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// ValidWarpReachDomain says whether s can be a site of the WARP reach list: a lower-case host name with at least two labels (no
// address, port or path), at most 253 characters. The panel checks the owner's list with it and the node checks again.
func ValidWarpReachDomain(s string) bool { return len(s) <= 253 && warpReachDomain.MatchString(s) }
