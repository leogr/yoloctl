package openshell

import (
	"bytes"
	"debug/elf"
	"encoding/binary"
	"testing"
)

// This is a non-executable ELF metadata fixture, never a replacement for
// source reproduction, artifact hashes or a real custom-base runtime proof.
func elfMetadataFixture(machine elf.Machine, interpreter string) []byte {
	header := make([]byte, 64)
	copy(header, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0})
	binary.LittleEndian.PutUint16(header[16:], uint16(elf.ET_EXEC))
	binary.LittleEndian.PutUint16(header[18:], uint16(machine))
	binary.LittleEndian.PutUint32(header[20:], 1)
	binary.LittleEndian.PutUint16(header[52:], 64)
	binary.LittleEndian.PutUint16(header[54:], 56)
	if interpreter == "" {
		return header
	}
	binary.LittleEndian.PutUint64(header[32:], 64)
	binary.LittleEndian.PutUint16(header[56:], 1)
	program := make([]byte, 56)
	binary.LittleEndian.PutUint32(program, uint32(elf.PT_INTERP))
	binary.LittleEndian.PutUint64(program[8:], 120)
	binary.LittleEndian.PutUint64(program[32:], uint64(len(interpreter)+1))
	return append(append(header, program...), append([]byte(interpreter), 0)...)
}

func TestELFContractRejectsWrongArchitectureAndMalformedLoader(t *testing.T) {
	for _, arch := range []struct {
		name    string
		machine elf.Machine
	}{{"amd64", elf.EM_X86_64}, {"arm64", elf.EM_AARCH64}} {
		raw := elfMetadataFixture(arch.machine, "")
		contract, err := ELFContract(bytes.NewReader(raw), arch.name)
		if err != nil || contract != "static" {
			t.Fatal(contract, err)
		}
		other := "amd64"
		if arch.name == other {
			other = "arm64"
		}
		if _, err := ELFContract(bytes.NewReader(raw), other); err == nil {
			t.Fatal("wrong architecture accepted")
		}
	}
	for _, loader := range []string{"relative", "/lib/../loader", "/loader;needed=none", "/bad\x00suffix"} {
		if _, err := ELFContract(bytes.NewReader(elfMetadataFixture(elf.EM_X86_64, loader)), "amd64"); err == nil {
			t.Fatal("malformed ELF interpreter accepted", loader)
		}
	}
	if _, err := ELFContract(bytes.NewReader([]byte("version 0.1.6")), "amd64"); err == nil {
		t.Fatal("version text accepted as an executable")
	}
	if _, err := ELFContract(bytes.NewReader(elfMetadataFixture(elf.EM_X86_64, "")), "riscv64"); err == nil {
		t.Fatal("unqualified architecture accepted")
	}
}
