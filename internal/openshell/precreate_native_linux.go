package openshell

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"runtime"
	"strings"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/spaceenv"
)

const precreateMetadataLimit = 8192

type precreateWireFile struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Mount  uint64 `json:"mount"`
}

func wirePrecreateFile(id pinFileIdentity) precreateWireFile {
	return precreateWireFile{id.device, id.inode, id.mount}
}
func (id precreateWireFile) identity() pinFileIdentity {
	return pinFileIdentity{id.Device, id.Inode, id.Mount}
}

// This envelope belongs only on the original private inherited worker channel.
// It contains public identity metadata, no CID, credential or authority token.
type precreateMetadata struct {
	Schema               int               `json:"schema"`
	ControllerGeneration string            `json:"controller_generation"`
	BootID               string            `json:"boot_id"`
	MountNamespace       namespaceIdentity `json:"mount_namespace"`
	NetworkNamespace     namespaceIdentity `json:"network_namespace"`
	Origin               containerOrigin   `json:"origin"`
	Collection           precreateWireFile `json:"collection"`
	Directory            precreateWireFile `json:"directory"`
	File                 precreateWireFile `json:"file"`
	OriginSHA256         string            `json:"origin_sha256"`
}

type precreateCurrentKernel struct {
	generation, boot string
	mount, network   namespaceIdentity
}

func newPrecreateMetadata(c precreateOriginClaim, k precreateCurrentKernel) precreateMetadata {
	return precreateMetadata{1, k.generation, k.boot, k.mount, k.network, c.origin, wirePrecreateFile(c.collection), wirePrecreateFile(c.directory), wirePrecreateFile(c.file), hex.EncodeToString(c.digest[:])}
}

func decodePrecreateMetadata(raw []byte) (precreateMetadata, error) {
	if len(raw) == 0 || len(raw) > precreateMetadataLimit || checkJSONKeys(raw) != nil {
		return precreateMetadata{}, errors.New("invalid precreate metadata envelope")
	}
	raw = append([]byte(nil), raw...)
	var w precreateMetadata
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&w); err != nil {
		return precreateMetadata{}, errors.New("invalid precreate metadata fields")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return precreateMetadata{}, errors.New("trailing precreate metadata")
	}
	// The one compiled producer always uses json.Marshal. Canonical equality
	// also refuses case-folded aliases, duplicate aliases, nulls and omitted
	// fields that encoding/json's typed decoding otherwise accepts.
	canonical, err := json.Marshal(w)
	if err != nil || !bytes.Equal(canonical, raw) {
		return precreateMetadata{}, errors.New("noncanonical or aliased precreate metadata")
	}
	return w, nil
}

func (w precreateMetadata) claim(expected containerOrigin, k precreateCurrentKernel) (precreateOriginClaim, error) {
	if len(k.generation) > 256 || strings.IndexFunc(k.generation, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return precreateOriginClaim{}, errors.New("invalid current precreate controller identity")
	}
	if w.Schema != 1 || k.generation == "" || w.ControllerGeneration != k.generation || w.BootID != k.boot || w.MountNamespace != k.mount || w.NetworkNamespace != k.network || k.mount.Device == 0 || k.mount.Inode == 0 || k.network.Device == 0 || k.network.Inode == 0 {
		return precreateOriginClaim{}, errors.New("precreate metadata belongs to another current controller/kernel")
	}
	if got, err := decodeKernelBootID([]byte(k.boot + "\n")); err != nil || got != k.boot {
		return precreateOriginClaim{}, errors.New("precreate metadata lacks current boot evidence")
	}
	if !lowerHex(w.OriginSHA256, 32) {
		return precreateOriginClaim{}, errors.New("invalid precreate origin digest")
	}
	digest, _ := hex.DecodeString(w.OriginSHA256)
	c := precreateOriginClaim{origin: w.Origin, collection: w.Collection.identity(), directory: w.Directory.identity(), file: w.File.identity()}
	copy(c.digest[:], digest)
	if err := c.validate(expected); err != nil {
		return precreateOriginClaim{}, err
	}
	return c, nil
}

func precreateExpectedOrigin(box, incarnation string, release QualifiedRelease) (containerOrigin, error) {
	if layout.ValidateName("agentbox", box) != nil || !lowerHex(incarnation, 16) {
		return containerOrigin{}, errors.New("invalid independently selected precreate identity")
	}
	if err := release.ValidateHelperSource(); err != nil {
		return containerOrigin{}, err
	}
	helper, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return containerOrigin{}, err
	}
	current := spaceenv.Current()
	return containerOrigin{current.Space, current.DiskID, current.DiskUUID, box, incarnation, release.Identity(), helper.BinarySHA256}, nil
}

func nativePrecreateBoundary(verifyCurrent func(context.Context) error) error {
	if !spaceenv.Native() || os.Geteuid() != 0 || os.Getegid() != 0 || os.Getpid() == 1 || verifyCurrent == nil {
		return errors.New("precreate observation requires admitted native VM-root/current-storage callback")
	}
	return nil
}

// PrecreateMetadata reports original reservation metadata under current native
// storage and genuine VM infrastructure kernel observations. It never publishes
// an endpoint or authorizes Create. The actual admitted worker must send it only
// on its original inherited checkpoint channel; a copied envelope grants none
// of that channel's worker/receipt/effect/phase authority.
func (r *NativeReservation) PrecreateMetadata(ctx context.Context, release QualifiedRelease, verifyCurrent func(context.Context) error) ([]byte, error) {
	if err := nativePrecreateBoundary(verifyCurrent); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := verifyCurrent(ctx); err != nil {
		return nil, err
	}
	c, err := r.precreateClaim()
	if err != nil {
		return nil, err
	}
	expected, err := precreateExpectedOrigin(c.origin.Box, c.origin.Incarnation, release)
	if err != nil {
		return nil, err
	}
	if err := c.validate(expected); err != nil {
		return nil, err
	}
	var raw []byte
	err = withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) error {
		if err := verifyCurrent(ctx); err != nil {
			return err
		}
		if err := r.Verify(); err != nil {
			return err
		}
		w := newPrecreateMetadata(c, precreateCurrentKernel{spaceenv.Current().Generation, k.bootID, k.mountID, k.networkID})
		var err error
		raw, err = json.Marshal(w)
		if err != nil {
			return err
		}
		if len(raw) > precreateMetadataLimit {
			return errors.New("precreate metadata exceeds bound")
		}
		if err := r.Verify(); err != nil {
			return err
		}
		if err := k.revalidate(ctx); err != nil {
			return err
		}
		if err := verifyCurrent(ctx); err != nil {
			return err
		}
		return ctx.Err()
	})
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// NativePrecreateSelection is copied, nonsecret observation. Possessing it
// cannot reconstruct a live worker, receipt, reservation or publication ACK.
type NativePrecreateSelection struct{ Space, DiskID, DiskUUID, ControllerGeneration, Box, Incarnation, Qualification, HelperSHA256 string }

// NativePrecreateRead is borrowed for one locked infrastructure-thread callback.
// It does not itself own mutation permission. The actual parent must bind its
// original worker/receipt/effect scope independently before any publisher effect.
type NativePrecreateRead struct {
	origin        *precreateOriginObservation
	kernel        *kernelInfrastructure
	verifyCurrent func(context.Context) error
	selection     NativePrecreateSelection
	diskMount     string
	active        bool
}

func (*NativePrecreateRead) String() string     { return "native precreate read scope [not authority]" }
func (s *NativePrecreateRead) GoString() string { return s.String() }

func (s *NativePrecreateRead) Check(ctx context.Context) error {
	if s == nil || !s.active || s.origin == nil || s.kernel == nil || s.verifyCurrent == nil {
		return errors.New("native precreate read scope is expired/incomplete")
	}
	if err := nativePrecreateBoundary(s.verifyCurrent); err != nil {
		return err
	}
	c := spaceenv.Current()
	v := s.selection
	if c.Space != v.Space || c.DiskID != v.DiskID || c.DiskUUID != v.DiskUUID || c.Generation != v.ControllerGeneration || c.DiskMount != s.diskMount {
		return errors.New("native precreate current storage/controller selection changed")
	}
	if err := s.verifyCurrent(ctx); err != nil {
		return err
	}
	if err := s.kernel.revalidate(ctx); err != nil {
		return err
	}
	if err := s.origin.check(); err != nil {
		return err
	}
	if err := s.kernel.revalidate(ctx); err != nil {
		return err
	}
	if err := s.verifyCurrent(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *NativePrecreateRead) Selection(ctx context.Context) (NativePrecreateSelection, error) {
	if err := s.Check(ctx); err != nil {
		return NativePrecreateSelection{}, err
	}
	return s.selection, nil
}

// WithNativePrecreateObservation independently derives the protected root,
// current native disk/controller and genuine VM boot/namespace observations.
// expectedBox and verifyCurrent must come from the ACTUAL parent execution's
// independently admitted scope, never the worker envelope. This read-only
// wrapper grants no original worker/receipt/effect or publication authority.
// It is for fresh pre-CID creation only; historical restoration is separate and
// must not require the current helper source merely to restore old routing.
func WithNativePrecreateObservation(ctx context.Context, raw []byte, expectedBox string, release QualifiedRelease, verifyCurrent func(context.Context) error, use func(*NativePrecreateRead) error) error {
	if err := nativePrecreateBoundary(verifyCurrent); err != nil {
		return err
	}
	if use == nil {
		return errors.New("native precreate observation lacks its borrowed read callback")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifyCurrent(ctx); err != nil {
		return err
	}
	w, err := decodePrecreateMetadata(raw)
	if err != nil {
		return err
	}
	expected, err := precreateExpectedOrigin(expectedBox, w.Origin.Incarnation, release)
	if err != nil {
		return err
	}
	return withNativeKernelInfrastructure(ctx, func(k *kernelInfrastructure) (err error) {
		c, err := w.claim(expected, precreateCurrentKernel{spaceenv.Current().Generation, k.bootID, k.mountID, k.networkID})
		if err != nil {
			return err
		}
		root, err := layout.OpenShellControlDir()
		if err != nil {
			return err
		}
		p, err := observePrecreateOriginAt(root, c, expected, 0, 0)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, p.Close()) }()
		current := spaceenv.Current()
		s := &NativePrecreateRead{origin: p, kernel: k, verifyCurrent: verifyCurrent, diskMount: current.DiskMount, active: true, selection: NativePrecreateSelection{current.Space, current.DiskID, current.DiskUUID, current.Generation, expected.Box, expected.Incarnation, expected.Qualification, expected.HelperSHA256}}
		defer func() { s.active = false; s.origin = nil; s.kernel = nil; s.verifyCurrent = nil }()
		if err := s.Check(ctx); err != nil {
			return err
		}
		if err := use(s); err != nil {
			return err
		}
		return s.Check(ctx)
	})
}
