package deploy

import (
	"debug/elf"
	"fmt"
)

var elfMachines = map[string]elf.Machine{
	"amd64":   elf.EM_X86_64,
	"386":     elf.EM_386,
	"arm64":   elf.EM_AARCH64,
	"arm":     elf.EM_ARM,
	"riscv64": elf.EM_RISCV,
	"mips":    elf.EM_MIPS,
	"mips64":  elf.EM_MIPS,
	"ppc64le": elf.EM_PPC64,
	"s390x":   elf.EM_S390,
	"loong64": elf.EM_LOONGARCH,
}

func binaryMatchesArch(path, arch string) (bool, error) {
	want, known := elfMachines[arch]
	if !known {

		return true, nil
	}

	file, err := elf.Open(path)
	if err != nil {
		return true, nil
	}
	defer file.Close()

	if file.Machine != want {
		return false, fmt.Errorf("binary is %s, server is %s (%s)",
			file.Machine, arch, want)
	}
	return true, nil
}
