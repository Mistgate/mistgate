package awgprep

import "regexp"

// ownDKMS finds the failure of the AmneziaWG module's own DKMS build in the output of dpkg or apt: dkms points at the make.log of
// this module, or dpkg names the module's package. A failure of another DKMS module (the output may mention amneziawg while it
// builds fine) is not this. The OS update job (internal/node/osupdate) asks the same question the same way.
var ownDKMS = regexp.MustCompile(`dkms/amneziawg/[0-9A-Za-z._+~-]+/build/make\.log|error processing package amneziawg(?:-dkms)?(?::[a-z0-9-]+)? `)

// OwnDKMSFailure reports whether apt or dpkg output shows the AmneziaWG module's own DKMS build failing.
func OwnDKMSFailure(out string) bool { return ownDKMS.MatchString(out) }

// kernelInDkms finds the kernel a dkms failure names ("... module build on kernel: 7.0.0-38-generic ...").
var kernelInDkms = regexp.MustCompile(`module build on kernel: ([0-9A-Za-z._+-]{1,128})|auto installation service for kernel ([0-9A-Za-z._+-]{1,128})`)

// DKMSFailureKernel is the kernel release the dkms failure in the output is for, "" when it does not name one.
func DKMSFailureKernel(out string) string {
	if m := kernelInDkms.FindStringSubmatch(out); m != nil {
		return m[1] + m[2]
	}
	return ""
}
