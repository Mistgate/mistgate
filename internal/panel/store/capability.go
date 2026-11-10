package store

// CapabilityPresent reports whether an agent capability list contains want.
func CapabilityPresent(caps []string, want string) bool {
	for _, capability := range caps {
		if capability == want {
			return true
		}
	}
	return false
}
