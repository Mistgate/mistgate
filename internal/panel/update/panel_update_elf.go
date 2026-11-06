package update

import (
	"debug/elf"
	"fmt"
)

func verifyPanelELF(path, arch string) error {
	f, err := elf.Open(path)
	if err != nil {
		return fmt.Errorf("panel release is not a Linux ELF executable: %w", err)
	}
	defer f.Close()
	want := elf.EM_NONE
	switch arch {
	case "amd64":
		want = elf.EM_X86_64
	case "arm64":
		want = elf.EM_AARCH64
	default:
		return fmt.Errorf("panel self-update does not support architecture %q", arch)
	}
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != want {
		return fmt.Errorf("panel release architecture mismatch: got %s/%s/%s", f.Class, f.Data, f.Machine)
	}
	return nil
}
