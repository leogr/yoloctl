// Package openshell owns the version-locked supervisor/boundary integration.
// Bootstrap material is infrastructure authority, never workload configuration.
package openshell

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/leogr/yoloctl/internal/layout"
	"golang.org/x/crypto/ssh"
)

const (
	// BootstrapSchema is a yoloctl helper contract, independent of the upstream
	// protocol version. Schema 2 requires the matched patch-2 binary pair.
	BootstrapSchema = 2
	controlSocket   = "/.openshell/channel/control/supervisor/control.sock"
	boundaryCert    = "/.openshell/channel/sandbox/server.crt"
	boundaryKey     = "/.openshell/channel/sandbox/server.key"
	issuerID        = "yoloctl"
	issuerKeyID     = "yoloctl-1"
)

// Binding is recorded only after the controller verifies native disk identity
// and while it owns the mutation gate. A changed field changes the generation.
// Constructing a Binding does not verify storage, grants or a live container.
type Binding struct {
	Space                string `json:"space"`
	DiskID               string `json:"disk_id"`
	DiskUUID             string `json:"disk_uuid"`
	Box                  string `json:"box"`
	Incarnation          string `json:"incarnation"`
	ControllerGeneration string `json:"controller_generation"`
	Qualification        string `json:"qualification"`
	PolicySHA256         string `json:"policy_sha256"`
}

func (b Binding) Validate() error {
	if err := layout.ValidateExistingSpaceName(b.Space); err != nil {
		return err
	}
	if err := layout.ValidateName("agentbox", b.Box); err != nil {
		return err
	}
	for _, v := range []string{b.DiskID, b.DiskUUID, b.ControllerGeneration, b.Qualification} {
		if v == "" || len(v) > 256 || strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
			return errors.New("runtime binding requires bounded nonempty identity tokens")
		}
	}
	if !lowerHex(b.Incarnation, 16) || !lowerHex(b.PolicySHA256, 32) {
		return errors.New("invalid runtime incarnation or policy identity")
	}
	return nil
}

func lowerHex(s string, bytes int) bool {
	v, err := hex.DecodeString(s)
	return err == nil && len(v) == bytes && hex.EncodeToString(v) == s
}

func (b Binding) digest() (string, error) {
	if err := b.Validate(); err != nil {
		return "", err
	}
	raw, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("yoloctl-openshell-bootstrap-v2\x00"), raw...))
	return hex.EncodeToString(sum[:]), nil
}

// BootstrapInput contains workload-only environment, in addition to nonsecret
// identity. String and GoString deliberately do not expose the environment.
type BootstrapInput struct {
	Binding         Binding
	UID, GID        uint32
	Workdir         string
	ReadOnlyWorkdir bool
	Environment     map[string]string
}

func (BootstrapInput) String() string     { return "OpenShell bootstrap input [redacted]" }
func (b BootstrapInput) GoString() string { return b.String() }

func (b BootstrapInput) validate() error {
	if err := b.Binding.Validate(); err != nil {
		return err
	}
	if b.UID == 0 || b.GID == 0 || b.UID == ^uint32(0) || b.GID == ^uint32(0) {
		return errors.New("workload UID/GID must be nonzero real identities, not setresid sentinels")
	}
	if b.UID == SupervisorUID || b.GID == SupervisorGID {
		return errors.New("sandbox identity collides with the reserved OpenShell supervisor identity")
	}
	if !path.IsAbs(b.Workdir) || path.Clean(b.Workdir) != b.Workdir || b.Workdir == "/" || strings.IndexFunc(b.Workdir, unicode.IsControl) >= 0 {
		return errors.New("runtime workdir must be an explicit normalized absolute directory")
	}
	for k, v := range b.Environment {
		if k == "" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return errors.New("invalid workload environment entry")
		}
	}
	return nil
}

type workloadIdentity struct {
	UID               uint32   `json:"uid"`
	GID               uint32   `json:"gid"`
	SupplementaryGIDs []uint32 `json:"supplementary_gids"`
	Source            string   `json:"source"`
	ResourceDigest    string   `json:"resource_digest"`
}
type evidence struct {
	Generation string `json:"generation"`
	Digest     string `json:"evidence_digest"`
}
type serverTLS struct {
	CertificateChainPath string `json:"certificate_chain_path"`
	PrivateKeyPath       string `json:"private_key_path"`
}
type listener struct {
	Kind       string    `json:"kind"`
	SocketPath string    `json:"socket_path"`
	TLS        serverTLS `json:"tls"`
}
type clientTLS struct {
	ServerName     string `json:"server_name"`
	TrustAnchorPEM string `json:"trust_anchor_pem"`
}
type transport struct {
	Kind       string `json:"kind"`
	SocketPath string `json:"socket_path"`
}
type verificationKey struct {
	KeyID        string `json:"key_id"`
	PublicKeyPEM string `json:"public_key_pem"`
}
type boundaryConfig struct {
	BoundaryID           string            `json:"boundary_id"`
	Generation           string            `json:"generation"`
	SessionID            string            `json:"session_id"`
	SessionRotation      uint64            `json:"session_rotation"`
	AuthEpoch            uint64            `json:"auth_epoch"`
	GatewayID            string            `json:"gateway_id"`
	VerificationKeys     []verificationKey `json:"verification_keys"`
	Listener             listener          `json:"listener"`
	ResourceClaims       map[string]string `json:"resource_claims"`
	WorkloadIdentity     workloadIdentity  `json:"workload_identity"`
	ProvisioningEvidence evidence          `json:"provisioning_evidence"`
	ChildEnv             map[string]string `json:"child_env"`
}

func (boundaryConfig) String() string     { return "OpenShell boundary configuration [redacted]" }
func (b boundaryConfig) GoString() string { return b.String() }

type runtimeDescriptor struct {
	BoundaryID           string            `json:"boundary_id"`
	Generation           string            `json:"generation"`
	SessionID            string            `json:"session_id"`
	WorkloadIdentity     workloadIdentity  `json:"workload_identity"`
	Transport            transport         `json:"transport"`
	TLS                  clientTLS         `json:"tls"`
	ResourceClaims       map[string]string `json:"resource_claims"`
	ProvisioningEvidence evidence          `json:"provisioning_evidence"`
}
type authBundle struct {
	SessionID         string `json:"session_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	SessionRotation   uint64 `json:"session_rotation"`
	AuthEpoch         uint64 `json:"auth_epoch"`
	GatewayToken      string `json:"gateway_token"`
	GatewayExpiresAt  int64  `json:"gateway_expires_at"`
	SandboxToken      string `json:"sandbox_token"`
	SandboxExpiresAt  int64  `json:"sandbox_expires_at"`
	SSHHostPrivateKey string `json:"ssh_host_private_key"`
}

func (authBundle) String() string     { return "OpenShell supervisor credentials [redacted]" }
func (b authBundle) GoString() string { return b.String() }

// Bootstrap is intentionally opaque. Its files must be published by the native
// root-owned provisioner into the separate boundary/supervisor mount views.
// It never contains the issuer's private signing key or the discarded TLS CA key.
type Bootstrap struct {
	boundary                boundaryConfig
	descriptor              runtimeDescriptor
	auth                    authBundle
	certificate, privateKey []byte
	sshPublicKey            []byte
	workdir                 string
	readOnly                bool
}

func (*Bootstrap) String() string     { return "OpenShell bootstrap [redacted]" }
func (b *Bootstrap) GoString() string { return b.String() }

// NewBootstrap mints per-runtime credentials using Go cryptography. The issuer
// key is supplied only by VM-root infrastructure, outside all workload binds.
// Nothing here launches a process, writes files or accepts a user override.
func NewBootstrap(issuer ed25519.PrivateKey, in BootstrapInput, now time.Time) (*Bootstrap, error) {
	if len(issuer) != ed25519.PrivateKeySize || subtle.ConstantTimeCompare(issuer, ed25519.NewKeyFromSeed(issuer[:ed25519.SeedSize])) != 1 {
		return nil, errors.New("invalid issuer signing key")
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	digest, err := in.Binding.digest()
	if err != nil {
		return nil, err
	}
	session, err := newUUID()
	if err != nil {
		return nil, err
	}
	// Every explicit start has a fresh generation, even when the container's
	// binding is unchanged. A token from before restart must not authenticate
	// the replacement boundary. The session nonce stays in the root-owned record.
	generation, err := generationForSession(digest, session)
	if err != nil {
		return nil, err
	}
	serverName := "sandbox." + session + ".openshell.internal"
	ca, cert, key, err := mintTLS(serverName, now)
	if err != nil {
		return nil, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(issuer.Public())
	if err != nil {
		return nil, err
	}
	publicPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}))
	claims := map[string]string{
		"yoloctl.space": in.Binding.Space, "yoloctl.disk-id": in.Binding.DiskID,
		"yoloctl.disk-uuid": in.Binding.DiskUUID, "yoloctl.box": in.Binding.Box,
		"yoloctl.incarnation": in.Binding.Incarnation, "yoloctl.controller-generation": in.Binding.ControllerGeneration,
		"yoloctl.qualification": in.Binding.Qualification, "yoloctl.policy-sha256": in.Binding.PolicySHA256,
	}
	identity := workloadIdentity{UID: in.UID, GID: in.GID, SupplementaryGIDs: []uint32{}, Source: "yoloctl", ResourceDigest: "sha256:" + digest}
	proof := evidence{Generation: generation, Digest: digest}
	env := make(map[string]string, len(in.Environment)+1)
	for k, v := range in.Environment {
		// Preserve the existing private user-environment channel's reserved
		// namespace filtering for both canonical and later SSH processes.
		if strings.HasPrefix(k, "OPENSHELL_") {
			continue
		}
		env[k] = v
	}
	// Upstream defaults HOME to workspace. The explicit child-only environment
	// keeps yoloctl's existing separate sandbox home across canonical and exec
	// launches, without changing upstream environment precedence.
	if _, ok := env["HOME"]; !ok {
		env["HOME"] = "/home/sandbox"
	}
	// The boundary enforces a numeric workload identity; preserve yoloctl's
	// existing child-facing name rather than making USER the numeric UID.
	if _, ok := env["USER"]; !ok {
		env["USER"] = "sandbox"
	}
	b := &Bootstrap{
		boundary: boundaryConfig{BoundaryID: in.Binding.Box, Generation: generation, SessionID: session,
			SessionRotation: 1, AuthEpoch: 1, GatewayID: issuerID, VerificationKeys: []verificationKey{{issuerKeyID, publicPEM}},
			Listener:       listener{Kind: "runtime-initiated-unix", SocketPath: controlSocket, TLS: serverTLS{boundaryCert, boundaryKey}},
			ResourceClaims: claims, WorkloadIdentity: identity, ProvisioningEvidence: proof, ChildEnv: env},
		descriptor: runtimeDescriptor{BoundaryID: in.Binding.Box, Generation: generation, SessionID: session,
			WorkloadIdentity: identity, Transport: transport{"runtime-initiated-unix", controlSocket},
			TLS: clientTLS{serverName, string(ca)}, ResourceClaims: claims, ProvisioningEvidence: proof},
		certificate: cert, privateKey: key, workdir: in.Workdir, readOnly: in.ReadOnlyWorkdir,
	}
	gateway, err := signSessionToken(issuer, issuerID, in.Binding.Box, generation, "gateway", now)
	if err != nil {
		return nil, err
	}
	sandbox, err := signSessionToken(issuer, issuerID, in.Binding.Box, generation, "sandbox", now)
	if err != nil {
		return nil, err
	}
	_, sshKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	sshBlock, err := ssh.MarshalPrivateKey(sshKey, "yoloctl runtime")
	if err != nil {
		return nil, err
	}
	sshPublic, err := ssh.NewPublicKey(sshKey.Public())
	if err != nil {
		return nil, err
	}
	b.sshPublicKey = ssh.MarshalAuthorizedKey(sshPublic)
	b.auth = authBundle{SessionID: session, RuntimeGeneration: generation, SessionRotation: 1, AuthEpoch: 1,
		GatewayToken: gateway, SandboxToken: sandbox, SSHHostPrivateKey: opensshPEM(sshBlock.Bytes)}
	return b, nil
}

func newUUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	s := hex.EncodeToString(raw[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", s[:8], s[8:12], s[12:16], s[16:20], s[20:]), nil
}

func generationForSession(bindingDigest, session string) (string, error) {
	if !lowerHex(bindingDigest, 32) || !canonicalUUID(session) {
		return "", errors.New("invalid runtime binding/session identity")
	}
	launchDigest := sha256.Sum256([]byte(bindingDigest + "\x00" + session))
	// Upstream generation tokens are limited to 64 characters, including "g".
	return "g" + hex.EncodeToString(launchDigest[:24]), nil
}

func canonicalUUID(s string) bool {
	return len(s) == 36 && s[8] == '-' && s[13] == '-' && s[18] == '-' && s[23] == '-' &&
		lowerHex(strings.ReplaceAll(s, "-", ""), 16) && s != "00000000-0000-0000-0000-000000000000"
}

type sessionClaims struct {
	Issuer            string `json:"iss"`
	Subject           string `json:"sub"`
	Audience          string `json:"aud"`
	IssuedAt          int64  `json:"iat"`
	Expires           int64  `json:"exp"`
	ID                string `json:"jti"`
	SandboxID         string `json:"sandbox_id"`
	RuntimeGeneration string `json:"runtime_generation"`
	AuthEpoch         uint64 `json:"auth_epoch"`
	Component         string `json:"component"`
}

func signSessionToken(key ed25519.PrivateKey, issuer, box, generation, profile string, now time.Time) (string, error) {
	if profile != "gateway" && profile != "sandbox" {
		return "", errors.New("unsupported session token profile")
	}
	id, err := newUUID()
	if err != nil {
		return "", err
	}
	iss := "openshell-gateway:" + issuer
	audience := "openshell-sandbox"
	if profile == "gateway" {
		audience = iss
	}
	header, err := json.Marshal(struct {
		Algorithm string `json:"alg"`
		Type      string `json:"typ"`
		KeyID     string `json:"kid"`
	}{
		"EdDSA", "openshell-" + profile + "-session+jwt", issuerKeyID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(sessionClaims{Issuer: iss, Subject: "spiffe://openshell/sandbox/" + box,
		Audience: audience, IssuedAt: now.Unix(), Expires: 0, ID: id, SandboxID: box,
		RuntimeGeneration: generation, AuthEpoch: 1, Component: "openshell-supervisor"})
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(claims)
	signature := ed25519.Sign(key, []byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func mintTLS(serverName string, now time.Time) (caPEM, certPEM, keyPEM []byte, err error) {
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	serial := func() (*big.Int, error) {
		max := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1))
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return nil, err
		}
		return n.Add(n, big.NewInt(1)), nil
	}
	caSerial, err := serial()
	if err != nil {
		return nil, nil, nil, err
	}
	ca := &x509.Certificate{SerialNumber: caSerial, Subject: pkix.Name{CommonName: "OpenShell sandbox session CA"},
		NotBefore: now.Add(-time.Hour), NotAfter: time.Date(4096, 1, 1, 0, 0, 0, 0, time.UTC),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, caPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	leafSerial, err := serial()
	if err != nil {
		return nil, nil, nil, err
	}
	leaf := &x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: "OpenShell sandbox runtime"},
		DNSNames: []string{serverName}, NotBefore: ca.NotBefore, NotAfter: ca.NotAfter,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafPublic, caPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafPrivate)
	if err != nil {
		return nil, nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), nil
}

func opensshPEM(raw []byte) string {
	encoded := base64.StdEncoding.EncodeToString(raw)
	var b strings.Builder
	b.WriteString("-----BEGIN OPENSSH PRIVATE KEY-----\n")
	for len(encoded) > 70 {
		b.WriteString(encoded[:70])
		b.WriteByte('\n')
		encoded = encoded[70:]
	}
	b.WriteString(encoded)
	b.WriteString("\n-----END OPENSSH PRIVATE KEY-----\n")
	return b.String()
}
