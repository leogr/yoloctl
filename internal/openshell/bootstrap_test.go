package openshell

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func bootstrapInput() BootstrapInput {
	return BootstrapInput{
		Binding: Binding{Space: "trial", DiskID: strings.Repeat("1", 32), DiskUUID: "filesystem-id",
			Box: "test-box", Incarnation: strings.Repeat("2", 32), ControllerGeneration: "controller-generation",
			Qualification: "test-patch2", PolicySHA256: strings.Repeat("3", 64)},
		UID: 2000, GID: 2000, Workdir: "/workdir/project", ReadOnlyWorkdir: true,
		Environment: map[string]string{"API_KEY": "test-secret"},
	}
}

func TestBootstrapBoundTypedContractsAndRedaction(t *testing.T) {
	public, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	in := bootstrapInput()
	in.Environment["OPENSHELL_WORKSPACE_READ_ONLY"] = "false"
	b, err := NewBootstrap(issuer, in, now)
	if err != nil {
		t.Fatal(err)
	}
	replacement, err := NewBootstrap(issuer, in, now)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.boundary.Generation == b.boundary.Generation || replacement.boundary.SessionID == b.boundary.SessionID {
		t.Fatal("replacement runtime reused a previous token's generation/session")
	}
	if b.boundary.BoundaryID != in.Binding.Box || b.boundary.Generation != b.descriptor.Generation ||
		b.auth.RuntimeGeneration != b.boundary.Generation || len(b.boundary.Generation) > 64 {
		t.Fatal("boundary, descriptor and token generation are not bound")
	}
	if b.auth.SessionID != b.descriptor.SessionID || b.auth.SessionID != b.boundary.SessionID || len(b.auth.SessionID) != 36 {
		t.Fatal("session identity mismatch")
	}
	if b.boundary.Listener.Kind != "runtime-initiated-unix" || b.boundary.Listener.SocketPath != b.descriptor.Transport.SocketPath ||
		b.boundary.Listener.TLS.CertificateChainPath != boundaryCert || b.boundary.Listener.TLS.PrivateKeyPath != boundaryKey {
		t.Fatal("bootstrap must use the protected Unix control path")
	}
	if !reflect.DeepEqual(b.boundary.WorkloadIdentity, b.descriptor.WorkloadIdentity) ||
		!reflect.DeepEqual(b.boundary.ResourceClaims, b.descriptor.ResourceClaims) ||
		!reflect.DeepEqual(b.boundary.ProvisioningEvidence, b.descriptor.ProvisioningEvidence) ||
		!lowerHex(b.boundary.ProvisioningEvidence.Digest, 32) {
		t.Fatal("descriptor and boundary confirmation evidence differ")
	}
	if b.workdir != in.Workdir || !b.readOnly || b.boundary.ChildEnv["HOME"] != "/home/sandbox" || b.boundary.ChildEnv["USER"] != "sandbox" || b.boundary.ChildEnv["API_KEY"] != "test-secret" {
		t.Fatal("cwd/read-only intent or workload-only environment lost")
	}
	if _, ok := b.boundary.ChildEnv["OPENSHELL_WORKSPACE_READ_ONLY"]; ok {
		t.Fatal("reserved user environment escaped its existing filter")
	}
	in.Environment["API_KEY"] = "later"
	if b.boundary.ChildEnv["API_KEY"] != "test-secret" {
		t.Fatal("bootstrap aliases caller environment")
	}
	for _, value := range []any{in, b, b.boundary, b.auth} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, value), "test-secret") || strings.Contains(fmt.Sprintf(format, value), "later") || strings.Contains(fmt.Sprintf(format, value), b.auth.SandboxToken) {
				t.Fatal("bootstrap diagnostic formatting exposes workload environment")
			}
		}
	}
	for profile, token := range map[string]string{"sandbox": b.auth.SandboxToken, "gateway": b.auth.GatewayToken} {
		parts := strings.Split(token, ".")
		if len(parts) != 3 {
			t.Fatal("not a compact session JWT")
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil || !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), sig) {
			t.Fatal("invalid EdDSA signature")
		}
		headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
		if err != nil {
			t.Fatal(err)
		}
		var header map[string]string
		if err := json.Unmarshal(headerBytes, &header); err != nil {
			t.Fatal(err)
		}
		if len(header) != 3 || header["alg"] != "EdDSA" || header["typ"] != "openshell-"+profile+"-session+jwt" || header["kid"] != issuerKeyID {
			t.Fatal("wrong token profile")
		}
		claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil {
			t.Fatal(err)
		}
		var claims sessionClaims
		decoder := json.NewDecoder(bytes.NewReader(claimsBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&claims); err != nil {
			t.Fatal(err)
		}
		audience := "openshell-sandbox"
		if profile == "gateway" {
			audience = "openshell-gateway:yoloctl"
		}
		if claims.Issuer != "openshell-gateway:yoloctl" || claims.Subject != "spiffe://openshell/sandbox/test-box" || claims.Audience != audience ||
			claims.Expires != 0 || claims.IssuedAt != now.Unix() || claims.RuntimeGeneration != b.descriptor.Generation ||
			claims.SandboxID != in.Binding.Box || claims.AuthEpoch != 1 || claims.Component != "openshell-supervisor" || len(claims.ID) != 36 {
			t.Fatal("session token does not bind the complete runtime identity")
		}
	}
	for _, document := range []any{b.boundary, b.descriptor, b.auth} {
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(base64.StdEncoding.EncodeToString(issuer.Seed()))) {
			t.Fatal("issuer signing seed included in container material")
		}
	}
	descriptor, err := json.Marshal(b.descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(descriptor, []byte("test-secret")) || bytes.Contains(descriptor, []byte(b.auth.SandboxToken)) {
		t.Fatal("supervisor descriptor unexpectedly includes child environment or auth")
	}
}

func TestBootstrapTLSAndSSHKeysMatchPinnedPublicIdentity(t *testing.T) {
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	b, err := NewBootstrap(issuer, bootstrapInput(), now)
	if err != nil {
		t.Fatal(err)
	}
	certBlock, _ := pem.Decode(b.certificate)
	keyBlock, _ := pem.Decode(b.privateKey)
	caBlock, _ := pem.Decode([]byte(b.descriptor.TLS.TrustAnchorPEM))
	if certBlock == nil || keyBlock == nil || caBlock == nil {
		t.Fatal("missing TLS PEM material")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: b.descriptor.TLS.ServerName, CurrentTime: now}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(key.(ed25519.PrivateKey).Public(), cert.PublicKey) {
		t.Fatal("TLS private key mismatch")
	}
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "different.openshell.internal", CurrentTime: now}); err == nil {
		t.Fatal("wrong runtime TLS name accepted")
	}
	sshKey, err := ssh.ParsePrivateKey([]byte(b.auth.SSHHostPrivateKey))
	if err != nil {
		t.Fatal("generated SSH key is not parseable:", err)
	}
	if !bytes.Equal(ssh.MarshalAuthorizedKey(sshKey.PublicKey()), b.sshPublicKey) {
		t.Fatal("SSH public pin mismatch")
	}
	lines := strings.Split(b.auth.SSHHostPrivateKey, "\n")
	for _, line := range lines[1 : len(lines)-3] {
		if len(line) != 70 {
			t.Fatal("SSH key encoding must use OpenSSH's 70-column wrapping")
		}
	}
}

func TestBootstrapRefusesInvalidInputAndChangesGenerationOnBindingChanges(t *testing.T) {
	_, issuer, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	for _, mutate := range []func(*BootstrapInput){
		func(b *BootstrapInput) { b.UID = 0 }, func(b *BootstrapInput) { b.GID = 0 },
		func(b *BootstrapInput) { b.UID = SupervisorUID }, func(b *BootstrapInput) { b.GID = SupervisorGID },
		func(b *BootstrapInput) { b.UID = ^uint32(0) }, func(b *BootstrapInput) { b.GID = ^uint32(0) },
		func(b *BootstrapInput) { b.Workdir = "" }, func(b *BootstrapInput) { b.Workdir = "/" },
		func(b *BootstrapInput) { b.Workdir = "/workdir/../other" }, func(b *BootstrapInput) { b.Workdir = "relative" },
		func(b *BootstrapInput) { b.Binding.Box = "../box" }, func(b *BootstrapInput) { b.Binding.DiskUUID = "" },
		func(b *BootstrapInput) { b.Binding.Incarnation = "invalid" }, func(b *BootstrapInput) { b.Binding.PolicySHA256 = "stale" },
		func(b *BootstrapInput) { b.Environment["bad=name"] = "value" },
	} {
		in := bootstrapInput()
		mutate(&in)
		if _, err := NewBootstrap(issuer, in, now); err == nil {
			t.Fatal("invalid bootstrap input accepted")
		}
	}
	if _, err := NewBootstrap(nil, bootstrapInput(), now); err == nil {
		t.Fatal("absent issuer accepted")
	}
	corrupt := append(ed25519.PrivateKey(nil), issuer...)
	corrupt[len(corrupt)-1] ^= 1
	if _, err := NewBootstrap(corrupt, bootstrapInput(), now); err == nil {
		t.Fatal("corrupt issuer accepted")
	}
	base := bootstrapInput()
	digest, err := base.Binding.digest()
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*Binding){
		func(b *Binding) { b.Space = "other" }, func(b *Binding) { b.DiskID = "other" },
		func(b *Binding) { b.DiskUUID = "other" }, func(b *Binding) { b.Box = "other-box" },
		func(b *Binding) { b.Incarnation = strings.Repeat("4", 32) },
		func(b *Binding) { b.ControllerGeneration = "other" }, func(b *Binding) { b.Qualification = "other" },
		func(b *Binding) { b.PolicySHA256 = strings.Repeat("5", 64) },
	} {
		binding := base.Binding
		mutate(&binding)
		changed, err := binding.digest()
		if err != nil {
			t.Fatal(err)
		}
		if changed == digest {
			t.Fatal("runtime generation fails to bind an affected identity")
		}
	}
}
