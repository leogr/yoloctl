package openshell

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/leogr/yoloctl/internal/atomicfile"
	"github.com/leogr/yoloctl/internal/layout"
	"github.com/leogr/yoloctl/internal/safepath"
	"github.com/leogr/yoloctl/internal/spaceenv"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/unix"
)

// RuntimeRecord contains no environment or private credentials. The helper
// loads it only from the verified root-owned per-incarnation private bind.
// That bind is writable by PID 1 for the one-shot activation exchange; neither
// child receives it. Each selected authority file is hash/owner checked.
// Endpoint publication still requires actual artifact/mount/isolation checks.
type RuntimeRecord struct {
	Launch           LaunchConfig      `json:"launch"`
	Generation       string            `json:"generation"`
	SessionID        string            `json:"session_id"`
	Artifacts        PlatformArtifacts `json:"artifacts"`
	Files            map[string]string `json:"files"`
	SSHHostPublicKey string            `json:"ssh_host_public_key"`
}

// PreparedRuntime is bookkeeping for a new generation. It is not readiness,
// authority to start a container, or proof that a previous mutation succeeded.
type PreparedRuntime struct {
	ContainerRoot  string
	GenerationRoot string
	Record         RuntimeRecord
}

type containerOrigin struct {
	Space         string `json:"space"`
	DiskID        string `json:"disk_id"`
	DiskUUID      string `json:"disk_uuid"`
	Box           string `json:"box"`
	Incarnation   string `json:"incarnation"`
	Qualification string `json:"qualification"`
	HelperSHA256  string `json:"helper_sha256"`
}

// PrepareNative is used only inside native lifecycle mutation admission, after
// verifying a new/stopped container incarnation and the installed artifact pair.
// The caller owns the space/entity locks, receipts, rollback and launch seam.
// Preparation itself never launches a process or advertises an SSH endpoint.
func PrepareNative(ctx context.Context, in BootstrapInput, release QualifiedRelease, helperSHA256 string, unconfined bool, policy, rego []byte) (prepared PreparedRuntime, err error) {
	return prepareNative(ctx, in, release, helperSHA256, unconfined, policy, rego, true)
}

// prepareNative with strictSource=false serves an existing box's next
// generation: it keeps that box's installed, hash-pinned helper, so only a
// notice reports a helper from an earlier build (see checkExistingBoxHelper).
func prepareNative(ctx context.Context, in BootstrapInput, release QualifiedRelease, helperSHA256 string, unconfined bool, policy, rego []byte, strictSource bool) (prepared PreparedRuntime, err error) {
	if !spaceenv.Native() || os.Geteuid() != 0 {
		return prepared, errors.New("OpenShell preparation requires VM-root controller infrastructure")
	}
	if err = ctx.Err(); err != nil {
		return prepared, err
	}
	if strictSource {
		err = release.ValidateHelperSource()
	} else {
		err = release.checkExistingBoxHelper(in.Binding.Box)
	}
	if err != nil {
		return prepared, err
	}
	helperArtifact, err := release.Helper(runtime.GOARCH)
	if err != nil {
		return prepared, err
	}
	if helperSHA256 != helperArtifact.BinarySHA256 {
		return prepared, errors.New("native preparation requires the qualified separate static helper")
	}
	c := spaceenv.Current()
	if in.Binding.Space != c.Space || in.Binding.DiskID != c.DiskID || in.Binding.DiskUUID != c.DiskUUID || in.Binding.ControllerGeneration != c.Generation ||
		in.UID != uint32(c.UID) || in.GID != uint32(c.GID) {
		return prepared, errors.New("runtime bootstrap does not match the verified native space identity")
	}
	launch, err := NewLaunchConfig(in, release, helperSHA256, unconfined)
	if err != nil {
		return prepared, err
	}
	artifacts, err := release.Platform(runtime.GOARCH)
	if err != nil {
		return prepared, err
	}
	if sha256Hex(policy) != in.Binding.PolicySHA256 || sha256Hex(rego) != release.document.Pins.RegoSHA256 {
		return prepared, errors.New("runtime policy/Rego differs from the selected launch qualification")
	}
	if err = launch.ValidateWorkspacePolicy(policy); err != nil {
		return prepared, err
	}
	issuer, err := nativeIssuer()
	if err != nil {
		return prepared, err
	}
	bootstrap, err := NewBootstrap(issuer, in, time.Now().UTC())
	if err != nil {
		return prepared, err
	}
	root, err := layout.OpenShellControlDir()
	if err != nil {
		return prepared, err
	}
	containerRel := "containers/" + in.Binding.Incarnation
	for _, relative := range []string{"containers", containerRel, containerRel + "/generations"} {
		if err = safepath.PrivateDirectory(root, relative, 0, 0); err != nil {
			return prepared, err
		}
	}
	containerRoot := filepath.Join(root, containerRel)
	origin := containerOrigin{in.Binding.Space, in.Binding.DiskID, in.Binding.DiskUUID, in.Binding.Box, in.Binding.Incarnation, release.Identity(), helperSHA256}
	originRaw, err := json.Marshal(origin)
	if err != nil {
		return prepared, err
	}
	if err = publishPrivateOnce(containerRoot, "origin.json", originRaw, 0, 0); err != nil && !errors.Is(err, os.ErrExist) {
		return prepared, err
	}
	previous, info, err := safepath.ReadFileInfo(containerRoot, "origin.json", 1<<20)
	if err != nil {
		return prepared, err
	}
	if err = privateFileOwner(info, 0, 0); err != nil {
		return prepared, err
	}
	if !bytes.Equal(previous, originRaw) {
		return prepared, errors.New("runtime incarnation already belongs to a different container/helper/qualification")
	}
	current, info, readErr := safepath.ReadFileInfo(containerRoot, "current.json", 1<<20)
	if readErr == nil {
		if err = privateFileOwner(info, 0, 0); err != nil {
			return prepared, err
		}
		if err = checkJSONKeys(current); err != nil {
			return prepared, err
		}
		var previousRecord RuntimeRecord
		decoder := json.NewDecoder(bytes.NewReader(current))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&previousRecord); err != nil {
			return prepared, err
		}
		if err = previousRecord.Validate(release, helperSHA256); err != nil {
			return prepared, err
		}
		prior := previousRecord.Launch.Binding
		if prior.Space != origin.Space || prior.DiskID != origin.DiskID || prior.DiskUUID != origin.DiskUUID || prior.Box != origin.Box || prior.Incarnation != origin.Incarnation {
			return prepared, errors.New("current runtime record belongs to a different container binding")
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return prepared, readErr
	}
	generation := bootstrap.descriptor.Generation
	generations := filepath.Join(containerRoot, "generations")
	created, err := freshGenerationDirectory(generations, generation)
	if !created {
		return prepared, err
	}
	generationRoot := filepath.Join(generations, generation)
	published := false
	defer func() {
		if err != nil && !published {
			// The directory was freshly created above, is protected by root's
			// private parent, and was never selected or exposed to a workload.
			cleanup := os.RemoveAll(generationRoot)
			if cleanup == nil {
				cleanup = atomicfile.SyncDir(generations)
			}
			if cleanup != nil {
				err = errors.Join(err, cleanup)
			}
		}
	}()
	if err != nil {
		return prepared, err
	}
	for _, directory := range []struct {
		name     string
		uid, gid int
	}{
		{"boundary", int(in.UID), int(in.GID)}, {"supervisor", int(SupervisorUID), int(SupervisorGID)}, {"status", 0, 0},
	} {
		if err = safepath.PrivateDirectory(generationRoot, directory.name, directory.uid, directory.gid); err != nil {
			return prepared, err
		}
	}
	boundaryJSON, err := json.Marshal(bootstrap.boundary)
	if err != nil {
		return prepared, err
	}
	descriptorJSON, err := json.Marshal(bootstrap.descriptor)
	if err != nil {
		return prepared, err
	}
	authJSON, err := json.Marshal(bootstrap.auth)
	if err != nil {
		return prepared, err
	}
	// This per-generation native/PID-1 authenticator never enters either
	// child view. It is distinct from supervisor/boundary JWT credentials.
	activationKey := make([]byte, 32)
	if _, err = rand.Read(activationKey); err != nil {
		return prepared, err
	}
	record := RuntimeRecord{Launch: launch, Generation: generation, SessionID: bootstrap.descriptor.SessionID, Artifacts: artifacts,
		Files: map[string]string{}, SSHHostPublicKey: string(bootstrap.sshPublicKey)}
	for _, file := range []struct {
		name     string
		body     []byte
		uid, gid int
	}{
		{"boundary/bootstrap.json", boundaryJSON, int(in.UID), int(in.GID)},
		{"boundary/server.crt", bootstrap.certificate, int(in.UID), int(in.GID)},
		{"boundary/server.key", bootstrap.privateKey, int(in.UID), int(in.GID)},
		{"supervisor/runtime-descriptor.json", descriptorJSON, int(SupervisorUID), int(SupervisorGID)},
		{"supervisor/auth.json", authJSON, int(SupervisorUID), int(SupervisorGID)},
		{"supervisor/policy.yaml", policy, int(SupervisorUID), int(SupervisorGID)},
		{"supervisor/sandbox-policy.rego", rego, int(SupervisorUID), int(SupervisorGID)},
		{"status/activation.key", activationKey, 0, 0},
	} {
		if err = safepath.WriteFileOwned(generationRoot, file.name, file.body, 0o600, file.uid, file.gid); err != nil {
			return prepared, err
		}
		record.Files[file.name] = sha256Hex(file.body)
	}
	if err = record.Validate(release, helperSHA256); err != nil {
		return prepared, err
	}
	recordRaw, err := json.Marshal(record)
	if err != nil {
		return prepared, err
	}
	if err = safepath.WriteFileOwned(generationRoot, "record.json", recordRaw, 0o600, 0, 0); err != nil {
		return prepared, err
	}
	if err = atomicfile.SyncDir(generationRoot); err != nil {
		return prepared, err
	}
	if err = ctx.Err(); err != nil {
		return prepared, err
	}
	// current.json is the last publication, after every credential and its
	// parent have been fsynced. A lost directory fsync retains the generation
	// for receipt reconciliation; it never deletes possibly selected material.
	err = safepath.WriteFileOwned(containerRoot, "current.json", recordRaw, 0o600, 0, 0)
	var uncertain *atomicfile.CommitError
	if err == nil || errors.As(err, &uncertain) {
		published = true
	}
	if err != nil {
		return prepared, err
	}
	return PreparedRuntime{ContainerRoot: containerRoot, GenerationRoot: generationRoot, Record: record}, nil
}

func freshGenerationDirectory(parent, name string) (bool, error) {
	if len(name) != 49 || name[0] != 'g' || !lowerHex(name[1:], 24) {
		return false, errors.New("invalid fresh runtime generation")
	}
	fd, err := unix.Open(parent, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	defer func() { _ = unix.Close(fd) }()
	if err = unix.Mkdirat(fd, name, 0o700); err != nil {
		return false, err
	}
	return true, unix.Fsync(fd)
}

// Validate checks the record's complete nonsecret contract. This does not
// replace root ownership/read-only-bind checks or actual namespace readiness.
func (r RuntimeRecord) Validate(release QualifiedRelease, helperSHA256 string) error {
	if err := r.Launch.Validate(); err != nil {
		return err
	}
	if release.Identity() == "" || r.Launch.Binding.Qualification != release.Identity() || r.Launch.HelperSHA256 != helperSHA256 {
		return errors.New("runtime record/helper uses a mixed or unqualified release")
	}
	platform, err := release.Platform(runtime.GOARCH)
	if err != nil {
		return err
	}
	if platform != r.Artifacts {
		return errors.New("runtime record uses a different boundary/supervisor pair")
	}
	if len(r.Generation) != 49 || r.Generation[0] != 'g' || !lowerHex(r.Generation[1:], 24) || !canonicalUUID(r.SessionID) {
		return errors.New("invalid runtime generation/session identity")
	}
	digest, err := r.Launch.Binding.digest()
	if err != nil {
		return err
	}
	expected, err := generationForSession(digest, r.SessionID)
	if err != nil || expected != r.Generation {
		return errors.New("runtime generation is not bound to its session and native identity")
	}
	names := []string{"boundary/bootstrap.json", "boundary/server.crt", "boundary/server.key", "supervisor/runtime-descriptor.json", "supervisor/auth.json", "supervisor/policy.yaml", "supervisor/sandbox-policy.rego", "status/activation.key"}
	if len(r.Files) != len(names) {
		return errors.New("runtime file qualification is incomplete")
	}
	for _, name := range names {
		if !lowerHex(r.Files[name], 32) {
			return errors.New("runtime file qualification is incomplete")
		}
	}
	if r.Files["supervisor/policy.yaml"] != r.Launch.Binding.PolicySHA256 || r.Files["supervisor/sandbox-policy.rego"] != release.document.Pins.RegoSHA256 {
		return errors.New("runtime policy file identities differ from the selected launch")
	}
	key, _, options, rest, err := ssh.ParseAuthorizedKey([]byte(r.SSHHostPublicKey))
	if err != nil || len(options) != 0 || len(rest) != 0 || key.Type() != ssh.KeyAlgoED25519 {
		return errors.New("invalid runtime SSH public identity")
	}
	return nil
}
