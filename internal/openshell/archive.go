package openshell

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const (
	maxArchiveBytes = 256 << 20
	maxBinaryBytes  = 512 << 20
)

// Artifact describes one member of a reviewed release. Both hashes must come
// from the qualification record, never a freshly downloaded checksum file.
type Artifact struct {
	URL           string `json:"url"`
	ArchiveSHA256 string `json:"archive_sha256"`
	BinarySHA256  string `json:"binary_sha256"`
	Member        string `json:"member"`
}

func (a Artifact) validateHashes() error {
	if (a.Member != "openshell-sandbox" && a.Member != "openshell-supervisor" && a.Member != HelperMember) ||
		!lowerHex(a.ArchiveSHA256, 32) || !lowerHex(a.BinarySHA256, 32) {
		return errors.New("incomplete OpenShell binary/archive qualification")
	}
	return nil
}

// VerifyArchive streams one exact regular archive member into private staging.
// The caller must not publish staged bytes until this returns nil and the
// complete boundary/supervisor/Rego pair has been checked. It follows no paths,
// creates no archive-named files, and never executes an unverified binary.
func VerifyArchive(input io.Reader, artifact Artifact, privateStage io.Writer) error {
	if err := artifact.validateHashes(); err != nil {
		return err
	}
	compressed := &io.LimitedReader{R: input, N: maxArchiveBytes + 1}
	archiveHash := sha256.New()
	buffered := bufio.NewReader(io.TeeReader(compressed, archiveHash))
	gz, err := gzip.NewReader(buffered)
	if err != nil {
		return fmt.Errorf("OpenShell gzip archive: %w", err)
	}
	defer func() { _ = gz.Close() }()
	// A second gzip member or arbitrary trailing bytes is not another asset.
	gz.Multistream(false)
	decompressed := &io.LimitedReader{R: gz, N: maxBinaryBytes + (1 << 20) + 1}
	tr := tar.NewReader(decompressed)
	binaryHash := sha256.New()
	found := false
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("OpenShell tar archive: %w", err)
		}
		if found || header.Name != artifact.Member || header.Typeflag != tar.TypeReg || header.Linkname != "" {
			return errors.New("OpenShell archive must contain exactly the expected regular binary")
		}
		if header.Size <= 0 || header.Size > maxBinaryBytes {
			return errors.New("OpenShell binary exceeds qualified size bounds")
		}
		if _, err := io.CopyN(io.MultiWriter(privateStage, binaryHash), tr, header.Size); err != nil {
			return fmt.Errorf("stage OpenShell binary: %w", err)
		}
		found = true
	}
	if !found {
		return errors.New("OpenShell archive has no binary")
	}
	// Finish gzip CRC verification. Tar's end markers may be followed only by
	// bounded zero padding, never a hidden second archive or other payload.
	var padding [4096]byte
	for {
		n, err := decompressed.Read(padding[:])
		for _, v := range padding[:n] {
			if v != 0 {
				return errors.New("nonzero data after OpenShell tar end")
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("verify OpenShell gzip stream: %w", err)
		}
	}
	if decompressed.N == 0 || compressed.N == 0 {
		return errors.New("OpenShell archive exceeds size bounds")
	}
	if _, err := buffered.Peek(1); !errors.Is(err, io.EOF) {
		if err != nil {
			return fmt.Errorf("verify OpenShell archive end: %w", err)
		}
		return errors.New("trailing data or multiple gzip streams in OpenShell archive")
	}
	if hex.EncodeToString(archiveHash.Sum(nil)) != artifact.ArchiveSHA256 {
		return errors.New("OpenShell archive SHA256 mismatch")
	}
	if hex.EncodeToString(binaryHash.Sum(nil)) != artifact.BinarySHA256 {
		return errors.New("OpenShell binary SHA256 mismatch")
	}
	return nil
}
