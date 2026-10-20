package awgprep

import (
	_ "embed"
	"fmt"
	"strconv"
	"strings"
)

const (
	amneziaPPAURI     = "https://ppa.launchpadcontent.net/amnezia/ppa/ubuntu/"
	amneziaPPAKeyPath = "/etc/apt/keyrings/amnezia-ppa.asc"
	aptSourcesDir     = "/etc/apt/sources.list.d"
)

//go:embed amnezia-ppa.asc
var amneziaPPAKey []byte

func ubuntuPPAFiles(osRelease map[string]string) ([]FileOp, error) {
	codename := strings.TrimSpace(osRelease["VERSION_CODENAME"])
	if codename == "" {
		codename = strings.TrimSpace(osRelease["UBUNTU_CODENAME"])
	}
	if !validUbuntuCodename(codename) {
		return nil, &Unsupported{
			Code: CodeDistro,
			Msg:  "cannot add the Amnezia PPA: Ubuntu has no valid VERSION_CODENAME or UBUNTU_CODENAME",
		}
	}

	base := aptSourcesDir + "/amnezia-ubuntu-ppa-" + codename
	files := []FileOp{{Path: amneziaPPAKeyPath, Body: string(amneziaPPAKey)}}
	if ubuntuUsesDeb822(osRelease["VERSION_ID"]) {
		files = append(files,
			FileOp{Path: base + ".list", Remove: true},
			FileOp{Path: base + ".sources", Body: fmt.Sprintf(
				"Types: deb\nURIs: %s\nSuites: %s\nComponents: main\nSigned-By: %s\n",
				amneziaPPAURI, codename, amneziaPPAKeyPath)},
		)
		return files, nil
	}

	files = append(files, FileOp{Path: base + ".list", Body: fmt.Sprintf(
		"deb [signed-by=%s] %s %s main\n",
		amneziaPPAKeyPath, amneziaPPAURI, codename)})
	return files, nil
}

func validUbuntuCodename(codename string) bool {
	if codename == "" {
		return false
	}
	for _, r := range codename {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return true
}

// add-apt-repository writes deb822 .sources files from Ubuntu 24.04 (Noble) onward, and .list files before that.
func ubuntuUsesDeb822(versionID string) bool {
	parts := strings.Split(strings.TrimSpace(versionID), ".")
	if len(parts) < 2 {
		return false
	}
	major, errMajor := strconv.Atoi(parts[0])
	minor, errMinor := strconv.Atoi(parts[1])
	if errMajor != nil || errMinor != nil {
		return false
	}
	return major > 24 || (major == 24 && minor >= 4)
}
