//go:build race && !js

package health

// raceBuild: the test binary doubles as the VLESS client helper, so under -race the helper process is race-instrumented too.
const raceBuild = true
