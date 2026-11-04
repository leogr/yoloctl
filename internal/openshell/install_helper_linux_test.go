package openshell

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"debug/elf"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
)

// Producer metadata in a non-executable fixture exercises rejection before
// destructive apply. No fixture binary is ever executed or called qualified
// runtime proof. Actual helper reproduction/native custom-base proof is separate.
func helperELFMetadataFixture(machine elf.Machine, info *debug.BuildInfo) []byte {
	var blob bytes.Buffer
	header := make([]byte, 32)
	copy(header, []byte("\xff Go buildinf:"))
	header[14] = 8
	header[15] = 2
	blob.Write(header)
	var size [binary.MaxVarintLen64]byte
	for _, text := range []string{info.GoVersion, string(make([]byte, 16)) + info.String() + string(make([]byte, 16))} {
		n := binary.PutUvarint(size[:], uint64(len(text)))
		blob.Write(size[:n])
		blob.WriteString(text)
	}
	raw := append(elfMetadataFixture(machine, ""), make([]byte, 64)...)
	buildOffset := len(raw)
	raw = append(raw, blob.Bytes()...)
	stringsOffset := len(raw)
	table := []byte("\x00.shstrtab\x00.go.buildinfo\x00")
	raw = append(raw, table...)
	for len(raw)%8 != 0 {
		raw = append(raw, 0)
	}
	sectionsOffset := len(raw)
	raw = append(raw, make([]byte, 3*64)...)
	binary.LittleEndian.PutUint64(raw[32:], 64)
	binary.LittleEndian.PutUint64(raw[40:], uint64(sectionsOffset))
	binary.LittleEndian.PutUint16(raw[56:], 1)
	binary.LittleEndian.PutUint16(raw[58:], 64)
	binary.LittleEndian.PutUint16(raw[60:], 3)
	binary.LittleEndian.PutUint16(raw[62:], 1)
	program := raw[64:120]
	binary.LittleEndian.PutUint32(program, uint32(elf.PT_LOAD))
	binary.LittleEndian.PutUint32(program[4:], uint32(elf.PF_R|elf.PF_W))
	binary.LittleEndian.PutUint64(program[32:], uint64(len(raw)))
	binary.LittleEndian.PutUint64(program[40:], uint64(len(raw)))
	stringsSection := raw[sectionsOffset+64 : sectionsOffset+128]
	binary.LittleEndian.PutUint32(stringsSection, 1)
	binary.LittleEndian.PutUint32(stringsSection[4:], uint32(elf.SHT_STRTAB))
	binary.LittleEndian.PutUint64(stringsSection[24:], uint64(stringsOffset))
	binary.LittleEndian.PutUint64(stringsSection[32:], uint64(len(table)))
	buildSection := raw[sectionsOffset+128:]
	binary.LittleEndian.PutUint32(buildSection, 11)
	binary.LittleEndian.PutUint32(buildSection[4:], uint32(elf.SHT_PROGBITS))
	binary.LittleEndian.PutUint64(buildSection[8:], uint64(elf.SHF_ALLOC|elf.SHF_WRITE))
	binary.LittleEndian.PutUint64(buildSection[16:], uint64(buildOffset))
	binary.LittleEndian.PutUint64(buildSection[24:], uint64(buildOffset))
	binary.LittleEndian.PutUint64(buildSection[32:], uint64(blob.Len()))
	return raw
}

func helperInstallFixture(t *testing.T, mutate func(*debug.BuildInfo)) (QualifiedRelease, map[string][]byte) {
	t.Helper()
	d := releaseFixture(t)
	archives := map[string][]byte{}
	for arch, machine := range map[string]elf.Machine{"amd64": elf.EM_X86_64, "arm64": elf.EM_AARCH64} {
		info := staticHelperBuildFixture()
		for i := range info.Settings {
			if info.Settings[i].Key == "GOARCH" {
				info.Settings[i].Value = arch
			}
		}
		for _, m := range d.Pins.Helper.Inputs.Modules {
			info.Deps = append(info.Deps, &debug.Module{Path: m.Path, Version: m.Version, Sum: m.Sum})
		}
		if mutate != nil {
			mutate(info)
		}
		body := helperELFMetadataFixture(machine, info)
		var buffer bytes.Buffer
		gz := gzip.NewWriter(&buffer)
		tw := tar.NewWriter(gz)
		if err := tw.WriteHeader(&tar.Header{Name: HelperMember, Typeflag: tar.TypeReg, Size: int64(len(body)), Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		a := d.Pins.Helper.Platforms[arch]
		a.BinarySHA256 = sha256Hex(body)
		a.ArchiveSHA256 = sha256Hex(buffer.Bytes())
		d.Pins.Helper.Platforms[arch] = a
		archives[a.URL] = buffer.Bytes()
	}
	subject, _ := json.Marshal(d.Pins)
	d.Qualification.SubjectSHA256 = sha256Hex(subject)
	raw, _ := json.Marshal(d)
	r, err := LoadQualifiedRelease(raw, sha256Hex(raw))
	if err != nil {
		t.Fatal(err)
	}
	return r, archives
}

func helperTestRoot(t *testing.T, r QualifiedRelease) *os.File {
	t.Helper()
	root, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	t.Cleanup(func() {
		for _, a := range r.document.Pins.Helper.Platforms {
			_ = os.Chmod(filepath.Join(root.Name(), a.BinarySHA256), 0o700)
		}
	})
	return root
}

func TestStaticHelperInstallIsIndependentImmutableAndQualified(t *testing.T) {
	r, archives := helperInstallFixture(t, nil)
	root := helperTestRoot(t, r)
	install := func() error {
		return installHelperAt(t.Context(), root, r, "amd64", fixtureFetcher(archives), os.Geteuid(), os.Getegid())
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	a, _ := r.Helper("amd64")
	name := filepath.Join(root.Name(), a.BinarySHA256, HelperMember)
	before, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := install(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(name)
	if err != nil || !os.SameFile(before, after) {
		t.Fatal("helper installation replaced exact bytes", err)
	}
	if _, err := os.Stat(filepath.Join(root.Name(), r.Identity())); !os.IsNotExist(err) {
		t.Fatal("helper coupled its directory to the Rust qualification")
	}
	if err := os.Chmod(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(name); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(name), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := install(); err == nil {
		t.Fatal("missing helper file was silently repaired")
	}
}

func TestHelperRejectsStaticCLIAndUnqualifiedProducerBeforePublication(t *testing.T) {
	for name, mutate := range map[string]func(*debug.BuildInfo){
		"static-cli": func(i *debug.BuildInfo) { i.Path = "github.com/leogr/yoloctl/cmd/yoloctl" },
		"dynamic-recipe": func(i *debug.BuildInfo) {
			for n := range i.Settings {
				if i.Settings[n].Key == "CGO_ENABLED" {
					i.Settings[n].Value = "1"
				}
			}
		},
		"wrong-toolchain": func(i *debug.BuildInfo) { i.GoVersion = "go1.27.1" },
		"replaced-module": func(i *debug.BuildInfo) { i.Deps[0].Replace = &debug.Module{Path: "../untracked"} },
	} {
		t.Run(name, func(t *testing.T) {
			r, archives := helperInstallFixture(t, mutate)
			root := helperTestRoot(t, r)
			if err := installHelperAt(context.Background(), root, r, "amd64", fixtureFetcher(archives), os.Geteuid(), os.Getegid()); err == nil {
				t.Fatal("invalid helper published")
			}
			entries, err := os.ReadDir(root.Name())
			if err != nil || len(entries) != 0 {
				t.Fatal("invalid helper leaked staged/published bytes", entries, err)
			}
		})
	}
}
