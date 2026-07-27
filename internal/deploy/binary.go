package deploy

import (
	"debug/elf"
	"fmt"
)

// elfMachines maps the architectures detect reports to the ELF machine type a
// binary for that architecture must declare.
//
// Only the targets this project builds for are listed; anything else is
// treated as unknown and left to the caller to allow.
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

// binaryMatchesArch reports whether a local binary can execute on the detected
// server architecture.
//
// The router bundles a server binary built for its own architecture, which is
// right for the common case of an x86_64 router provisioning an x86_64 VPS and
// wrong the moment the server is an arm64 box. Uploading the wrong one
// installs something that cannot run: the service fails to start with a bare
// "exec format error" several steps later, which reads like a packaging fault
// rather than a mismatched download.
//
// An unreadable or non-ELF file is not treated as a mismatch — that is a
// different problem, and uploadBinary reports it with better context.
func binaryMatchesArch(path, arch string) (bool, error) {
	want, known := elfMachines[arch]
	if !known {
		// An architecture we have no mapping for: do not block the upload on
		// a guess.
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
