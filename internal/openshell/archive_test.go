package openshell

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func archiveFixture(t *testing.T, headers ...tar.Header) []byte {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	for _, header := range headers {
		if err := tw.WriteHeader(&header); err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			if _, err := tw.Write(bytes.Repeat([]byte{'b'}, int(header.Size))); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func hashBytes(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestArchiveVerifiesCompressedAndExtractedIdentity(t *testing.T) {
	body := archiveFixture(t, tar.Header{Name: "openshell-sandbox", Typeflag: tar.TypeReg, Size: 4, Mode: 0o755})
	artifact := Artifact{Member: "openshell-sandbox", ArchiveSHA256: hashBytes(body), BinarySHA256: hashBytes([]byte("bbbb"))}
	var stage bytes.Buffer
	if err := VerifyArchive(bytes.NewReader(body), artifact, &stage); err != nil {
		t.Fatal(err)
	}
	if stage.String() != "bbbb" {
		t.Fatal("wrong staged binary bytes")
	}
	for _, mutate := range []func(*Artifact){
		func(a *Artifact) { a.ArchiveSHA256 = hashBytes([]byte("different archive")) },
		func(a *Artifact) { a.BinarySHA256 = hashBytes([]byte("different binary")) },
	} {
		changed := artifact
		mutate(&changed)
		if err := VerifyArchive(bytes.NewReader(body), changed, &bytes.Buffer{}); err == nil {
			t.Fatal("unqualified archive/binary identity accepted")
		}
	}
	for _, modified := range [][]byte{body[:len(body)-1], append(append([]byte(nil), body...), body...), append(append([]byte(nil), body...), 'x')} {
		changed := artifact
		changed.ArchiveSHA256 = hashBytes(modified)
		if err := VerifyArchive(bytes.NewReader(modified), changed, &bytes.Buffer{}); err == nil {
			t.Fatal("truncated or concatenated archive accepted")
		}
	}
}

func TestArchiveRefusesEveryUnexpectedMemberAndLink(t *testing.T) {
	regular := tar.Header{Name: "openshell-sandbox", Typeflag: tar.TypeReg, Size: 4, Mode: 0o755}
	for _, headers := range [][]tar.Header{
		{}, {regular, regular}, {regular, {Name: "README", Typeflag: tar.TypeReg, Size: 1}},
		{{Name: "../openshell-sandbox", Typeflag: tar.TypeReg, Size: 4}},
		{{Name: "/openshell-sandbox", Typeflag: tar.TypeReg, Size: 4}},
		{{Name: "openshell-sandbox", Typeflag: tar.TypeSymlink, Linkname: "/outside"}},
		{{Name: "openshell-sandbox", Typeflag: tar.TypeLink, Linkname: "other"}},
		{{Name: "openshell-sandbox", Typeflag: tar.TypeDir}},
		{{Name: "openshell-sandbox", Typeflag: tar.TypeFifo}},
		{{Name: "openshell-sandbox", Typeflag: tar.TypeReg, Size: 0}},
	} {
		body := archiveFixture(t, headers...)
		artifact := Artifact{Member: "openshell-sandbox", ArchiveSHA256: hashBytes(body), BinarySHA256: hashBytes([]byte("bbbb"))}
		if err := VerifyArchive(bytes.NewReader(body), artifact, &bytes.Buffer{}); err == nil {
			t.Fatal("unsafe/unexpected archive member accepted:", headers)
		}
	}
}
