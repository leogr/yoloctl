package openshell

import (
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"
)

// ELFContract reports an artifact's exact Linux architecture, interpreter,
// linked libraries and imported symbol versions without executing it. The
// canonical value is retained in the reviewed platform/build proof. "static"
// means no interpreter and no DT_NEEDED dependencies; a version banner alone
// never establishes custom-base loader compatibility.
func ELFContract(reader io.ReaderAt, arch string) (string, error) {
	machine := elf.EM_X86_64
	switch arch {
	case "amd64":
	case "arm64":
		machine = elf.EM_AARCH64
	default:
		return "", errors.New("unsupported OpenShell ELF architecture")
	}
	f, err := elf.NewFile(reader)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	if f.Class != elf.ELFCLASS64 || f.Data != elf.ELFDATA2LSB || f.Machine != machine || (f.Type != elf.ET_EXEC && f.Type != elf.ET_DYN) || (f.OSABI != elf.ELFOSABI_NONE && f.OSABI != elf.ELFOSABI_LINUX) {
		return "", errors.New("OpenShell artifact is not a Linux executable for the selected architecture")
	}
	interpreter := ""
	for _, program := range f.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		if interpreter != "" || program.Filesz < 2 || program.Filesz > 4096 {
			return "", errors.New("invalid OpenShell ELF interpreter")
		}
		raw, err := io.ReadAll(io.LimitReader(program.Open(), 4097))
		if err != nil || uint64(len(raw)) != program.Filesz || raw[len(raw)-1] != 0 || strings.IndexByte(string(raw[:len(raw)-1]), 0) >= 0 {
			return "", errors.New("invalid OpenShell ELF interpreter path")
		}
		interpreter = string(raw[:len(raw)-1])
		if !path.IsAbs(interpreter) || path.Clean(interpreter) != interpreter || interpreter == "/" || strings.ContainsAny(interpreter, ";\n\r\t") {
			return "", errors.New("invalid OpenShell ELF interpreter path")
		}
	}
	needed, err := f.ImportedLibraries()
	if err != nil {
		return "", err
	}
	for _, library := range needed {
		if library == "" || len(library) > 256 || strings.ContainsAny(library, "/;,@\n\r\t\x00") {
			return "", errors.New("invalid OpenShell ELF library name")
		}
	}
	if interpreter == "" {
		if len(needed) != 0 {
			return "", errors.New("OpenShell ELF has library dependencies without an interpreter")
		}
		return "static", nil
	}
	imports, err := f.ImportedSymbols()
	if err != nil {
		return "", fmt.Errorf("OpenShell ELF symbol requirements: %w", err)
	}
	var versions []string
	for _, symbol := range imports {
		if symbol.Version == "" {
			continue
		}
		if strings.ContainsAny(symbol.Library+symbol.Version, ";,\n\r\t\x00") {
			return "", errors.New("invalid OpenShell ELF symbol version")
		}
		versions = append(versions, symbol.Library+"@"+symbol.Version)
	}
	slices.Sort(needed)
	slices.Sort(versions)
	return "interpreter=" + interpreter + ";needed=" + strings.Join(slices.Compact(needed), ",") + ";versions=" + strings.Join(slices.Compact(versions), ","), nil
}
