//go:build linux && amd64

package firecracker

import (
	"archive/tar"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/leogr/yoloctl/assets"
)

// Expected values are `openssl x509 -noout -subject_hash` (OpenSSL 3.0.13)
// for self-signed certificates carrying these encoded subjects.
func TestOpenSSLSubjectHash(t *testing.T) {
	for _, tc := range []struct{ name, subject, hash string }{
		{"plain", "3036310b30090603550406130255533110300e060355040a0c074578616d706c653115301306035504030c0c4578616d706c6520526f6f74", "52667f76"},
		{"whitespace-case", "303e311a3018060355040a131120204578616d706c652020204f524720203120301e06035504030c1709204d69786564200a0d204341534520204e616d65200b", "c6d60e78"},
		{"multi-valued", "302d311b300b060355040b0c047a657461300c060355040a0c05416c706861310e300c06035504030c056d756c7469", "737d9c94"},
		{"t61-latin1", "30133111300f0603550403140846fc6e6620204341", "4c814312"},
		{"bmp", "3021311f301d06035504031e1665e5672c0020002000c4004200200052006f006f0074", "728774b2"},
		{"universal", "302f312d302b06035504031c24000000550000006e000000690000002000000020000000430000004f0000004400000045", "7ceabc92"},
		{"ia5", "30383120301e06092a864886f70d010901161141646d696e404578616d706c652e434f4d3114301206035504030c0b4d6978656420204e414d45", "3587a45b"},
		{"numeric-copied", "301f310f300d06035504051206313220203334310c300a06035504030c034e756d", "df0eff6b"},
		{"utf8-nonascii", "30193117301506035504030c0ec389434f4c452020c3a974c3a920", "bc50f435"},
		{"empty", "3000", "eea339da"},
	} {
		subject, err := hex.DecodeString(tc.subject)
		if err != nil {
			t.Fatal(err)
		}
		got, err := opensslSubjectHash(subject)
		if err != nil || fmt.Sprintf("%08x", got) != tc.hash {
			t.Errorf("%s: hash %08x, %v; want %s", tc.name, got, err, tc.hash)
		}
		if _, err := certificateSubject(testCertificate(t, subject)); err != nil {
			t.Errorf("%s: subject walk: %v", tc.name, err)
		}
	}
	for _, subject := range []string{
		"300c300a300806035504030c0178",       // RDN is not a SET
		"300e310c300a06035504031e03004100",   // odd BMPString length
		"300d310b300906035504031e02d800",     // BMP surrogate
		"300d310b300906035504030c02c328",     // invalid UTF-8
		"3009310730050603550403",             // attribute without value
		"300f310d300b06035504031c0400110000", // beyond Unicode
		"300c310a300806035504030c017800",     // trailing data
	} {
		raw, err := hex.DecodeString(subject)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := opensslSubjectHash(raw); err == nil {
			t.Errorf("accepted invalid subject %s", subject)
		}
	}
}

func testCertificate(t *testing.T, subject []byte) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), RawSubject: subject, NotBefore: time.Unix(0, 0), NotAfter: time.Unix(1<<32, 0), IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func testPEM(t *testing.T, cn string) []byte {
	t.Helper()
	subject, err := asn1.Marshal(pkix.Name{CommonName: cn}.ToRDNSequence())
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: testCertificate(t, subject)})
}

func testHash(t *testing.T, data []byte) string {
	t.Helper()
	block, _ := pem.Decode(data)
	subject, err := certificateSubject(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	h, err := opensslSubjectHash(subject)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%08x", h)
}

func treeText(t *testing.T, tree imageTree, name string) string {
	t.Helper()
	data, err := tree.readRegular(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The base image's generated store is replaced by a fresh installation of the
// pinned package: retired roots disappear from files, links and the bundle.
func TestTrustStoreRegeneration(t *testing.T) {
	stale := testPEM(t, "Retired Root")
	tree := imageTree{}
	for _, dir := range []string{"etc/ssl/certs", mozillaCertsDir, localCertsDir} {
		tree.directory(dir)
	}
	tree.put(mozillaCertsDir+"/Retired_Root.crt", 0644, stale)
	tree.put(mozillaCertsDir+"/Kept_Root.crt", 0644, testPEM(t, "Kept Root"))
	tree.link(etcCertsDir+"/Retired_Root.pem", "/"+mozillaCertsDir+"/Retired_Root.crt")
	tree.link(etcCertsDir+"/Kept_Root.pem", "/"+mozillaCertsDir+"/Kept_Root.crt")
	tree.link(etcCertsDir+"/"+testHash(t, stale)+".0", "Retired_Root.pem")
	tree.put(certsBundle, 0644, stale)
	tree.put(certsConf, 0644, []byte("mozilla/Retired_Root.crt\nmozilla/Kept_Root.crt\n"))
	tree.link("usr/lib/ssl/cert.pem", "/"+certsBundle)
	if err := tree.dropManagedCertificates(); err != nil {
		t.Fatal(err)
	}
	for name := range tree {
		if strings.HasPrefix(name, mozillaCertsDir+"/") || strings.HasPrefix(name, etcCertsDir+"/") {
			t.Fatalf("stale store entry survived: %s", name)
		}
	}

	// Package extraction: a renamed root, same-subject roots, an exact copy and
	// a file without a final newline.
	kept := testPEM(t, "Kept Root")
	twinA, twinB := testPEM(t, "Twin"), testPEM(t, "Twin")
	odd := testPEM(t, "Odd Name")
	noNewline := []byte(strings.TrimSuffix(string(testPEM(t, "No Newline")), "\n"))
	pkg := map[string][]byte{
		"mozilla/Kept_Root.crt":      kept,
		"mozilla/Copy_Of_Kept.crt":   kept,
		"mozilla/Twin_B.crt":         twinB,
		"mozilla/Twin_A.crt":         twinA,
		"mozilla/Odd (Name), CA.crt": odd,
		"mozilla/no_newline.crt":     noNewline,
	}
	for name, data := range pkg {
		tree.put(caCertificatesDir+"/"+name, 0644, data)
	}
	if err := tree.rebuildTrustStore(); err != nil {
		t.Fatal(err)
	}

	order := []string{"mozilla/Copy_Of_Kept.crt", "mozilla/Kept_Root.crt", "mozilla/Odd (Name), CA.crt", "mozilla/Twin_A.crt", "mozilla/Twin_B.crt", "mozilla/no_newline.crt"}
	if got, want := treeText(t, tree, certsConf), certsConfHeader+strings.Join(order, "\n")+"\n"; got != want {
		t.Fatalf("configuration:\n%s\nwant:\n%s", got, want)
	}
	var bundle string
	for _, name := range order {
		bundle += string(pkg[name])
		if !strings.HasSuffix(bundle, "\n") {
			bundle += "\n"
		}
	}
	if treeText(t, tree, certsBundle) != bundle {
		t.Fatal("bundle does not follow the configuration with final newlines")
	}
	want := map[string]string{
		"Copy_Of_Kept.pem":    "/usr/share/ca-certificates/mozilla/Copy_Of_Kept.crt",
		"Kept_Root.pem":       "/usr/share/ca-certificates/mozilla/Kept_Root.crt",
		"Odd_=Name=__CA.pem":  "/usr/share/ca-certificates/mozilla/Odd (Name), CA.crt",
		"Twin_A.pem":          "/usr/share/ca-certificates/mozilla/Twin_A.crt",
		"Twin_B.pem":          "/usr/share/ca-certificates/mozilla/Twin_B.crt",
		"no_newline.pem":      "/usr/share/ca-certificates/mozilla/no_newline.crt",
		"ca-certificates.crt": "",
		// The duplicate is linked once, under the first name in strcmp order.
		testHash(t, kept) + ".0":      "Copy_Of_Kept.pem",
		testHash(t, odd) + ".0":       "Odd_=Name=__CA.pem",
		testHash(t, twinA) + ".0":     "Twin_A.pem",
		testHash(t, twinA) + ".1":     "Twin_B.pem",
		testHash(t, noNewline) + ".0": "no_newline.pem",
	}
	got := map[string]string{}
	for name, e := range tree {
		if path.Dir(name) == etcCertsDir {
			got[path.Base(name)] = e.header.Linkname
			if (e.header.Typeflag == tar.TypeSymlink) == (path.Base(name) == "ca-certificates.crt") {
				t.Fatalf("%s has type %c", name, e.header.Typeflag)
			}
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("certificate links:\n%v\nwant:\n%v", got, want)
	}
	if e := tree["usr/lib/ssl/cert.pem"]; e.header.Linkname != "/"+certsBundle {
		t.Fatal("OpenSSL default file no longer names the bundle")
	}
}

func TestTrustStoreRefusals(t *testing.T) {
	base := func() imageTree {
		tree := imageTree{}
		tree.directory(etcCertsDir)
		tree.put(mozillaCertsDir+"/Root.crt", 0644, testPEM(t, "Root"))
		return tree
	}
	for _, tc := range []struct {
		name  string
		setup func(imageTree)
	}{
		{"local CA", func(tree imageTree) { tree.put(localCertsDir+"/private.crt", 0644, testPEM(t, "Private")) }},
		{"unmanaged file", func(tree imageTree) { tree.put(etcCertsDir+"/ssl-cert-snakeoil.pem", 0644, testPEM(t, "Snakeoil")) }},
		{"unmanaged link", func(tree imageTree) { tree.link(etcCertsDir+"/other.pem", "/etc/other.pem") }},
		{"nested entry", func(tree imageTree) { tree.directory(etcCertsDir + "/java") }},
	} {
		tree := base()
		tc.setup(tree)
		if err := tree.dropManagedCertificates(); err == nil {
			t.Errorf("%s: unmanaged base content was discarded", tc.name)
		}
	}

	distrusted := testPEM(t, "Distrusted")
	block, _ := pem.Decode(distrusted)
	sum := sha256.Sum256(block.Bytes)
	distrustedRoots[hex.EncodeToString(sum[:])] = "test root"
	t.Cleanup(func() { delete(distrustedRoots, hex.EncodeToString(sum[:])) })
	for _, tc := range []struct {
		name  string
		setup func(imageTree)
	}{
		{"no certificates", func(tree imageTree) { delete(tree, mozillaCertsDir+"/Root.crt") }},
		{"leftover store", func(tree imageTree) { tree.link(etcCertsDir+"/Old.pem", "/"+mozillaCertsDir+"/Old.crt") }},
		{"distrusted root", func(tree imageTree) { tree.put(mozillaCertsDir+"/Distrusted.crt", 0644, distrusted) }},
		{"two certificates", func(tree imageTree) {
			tree.put(mozillaCertsDir+"/Pair.crt", 0644, append(testPEM(t, "One"), testPEM(t, "Two")...))
		}},
		{"not a certificate", func(tree imageTree) {
			tree.put(mozillaCertsDir+"/Key.crt", 0644, []byte("-----BEGIN PRIVATE KEY-----\nAA==\n-----END PRIVATE KEY-----\n"))
		}},
	} {
		tree := base()
		tc.setup(tree)
		if err := tree.rebuildTrustStore(); err == nil {
			t.Errorf("%s: regenerated an unsafe store", tc.name)
		}
	}
}

// The embedded recipe must carry Ubuntu's patched package; extraction alone
// leaves the base image's 2024 store, which trusts both retired roots.
func TestRecipePinsPatchedCACertificates(t *testing.T) {
	raw, err := assets.VMImageRecipe.ReadFile("vm/firecracker/runtime-pins.json")
	if err != nil {
		t.Fatal(err)
	}
	var pins map[string]imagePin
	if err := json.Unmarshal(raw, &pins); err != nil {
		t.Fatal(err)
	}
	pin, ok := pins["ca-certificates.deb"]
	if !ok || pin.URL != "https://archive.ubuntu.com/ubuntu/pool/main/c/ca-certificates/ca-certificates_20260816ubuntu0.24.04.1_all.deb" || pin.SHA256 != "8a0a754aaafed72cc8efb8c1731808133284a84fbfc76a4fcc73fce9e01b4187" {
		t.Fatalf("patched CA package is not pinned: %+v", pin)
	}
	if imageRecipeVersion < 2 {
		t.Fatal("trust-store assembly did not invalidate cached images")
	}
	if _, ok := distrustedRoots["86a1ecba089c4a8d3bbe2734c612ba341d813e043cf9e8a862cd5c57a36bbe6b"]; !ok {
		t.Fatal("emSign ECC Root CA - G3 is not refused")
	}
}
