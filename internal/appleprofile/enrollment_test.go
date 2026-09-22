package appleprofile

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"github.com/smallstep/scep/x509util"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/beevik/etree"
	"github.com/smallstep/pkcs7"
	"github.com/smallstep/scep"
)

type testIdentity struct {
	cert *x509.Certificate
	key  *rsa.PrivateKey
}

func identity(t *testing.T) testIdentity {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "fixture"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return testIdentity{cert, key}
}
func signTest(t *testing.T, i testIdentity, data []byte) []byte {
	t.Helper()
	s, err := pkcs7.NewSignedData(data)
	if err != nil {
		t.Fatal(err)
	}
	s.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := s.AddSigner(i.cert, i.key, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	b, err := s.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func startTest(t *testing.T, e *Enrollments) string {
	t.Helper()
	id, err := e.Start(Options{ServerURL: "https://drive.example", UserID: "owner", Email: "a&b@example.test", InstallationID: "phone", Calendars: true, Contacts: true}, "credential", "password<&>")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Cancel(id) })
	return id
}
func bootstrap(t *testing.T, e *Enrollments, id string, i testIdentity) []byte {
	t.Helper()
	request := signTest(t, i, plist("<dict>"+field("CHALLENGE", id)+"</dict>"))
	response, err := e.Profile(id, request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
func issueRequest(t *testing.T, e *Enrollments, id string, i testIdentity, challenge string) []byte {
	t.Helper()
	tpl := &x509util.CertificateRequest{CertificateRequest: x509.CertificateRequest{Subject: pkix.Name{CommonName: "untrusted client subject"}, DNSNames: []string{"not-authorized.example"}}, ChallengePassword: challenge}
	der, err := x509util.CreateCertificateRequest(rand.Reader, tpl, i.key)
	if err != nil {
		t.Fatal(err)
	}
	csr, err := x509.ParseCertificateRequest(der)
	if err != nil {
		t.Fatal(err)
	}
	msg, err := scep.NewCSRRequest(csr, &scep.PKIMessage{MessageType: scep.PKCSReq, SignerCert: i.cert, SignerKey: i.key, Recipients: []*x509.Certificate{e.ca}})
	if err != nil {
		t.Fatal(err)
	}
	return msg.Raw
}
func content(t *testing.T, data []byte) []byte {
	t.Helper()
	p, _, err := signedMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	return p.Content
}
func TestEnrollmentEndToEnd(t *testing.T) {
	var e Enrollments
	id := startTest(t, &e)
	device := identity(t)
	initial, err := e.Initial(id)
	if err != nil {
		t.Fatal(err)
	}
	if p := content(t, initial); bytes.Contains(p, []byte("password")) || !bytes.Contains(p, []byte("Profile Service")) {
		t.Fatal("invalid bootstrap")
	}
	boot := bootstrap(t, &e, id, device)
	if p := content(t, boot); !bytes.Contains(p, []byte("com.apple.security.scep")) || bytes.Contains(p, []byte("password<&>")) {
		t.Fatal("invalid identity profile")
	}
	req := issueRequest(t, &e, id, device, e.sessions[id].challenge)
	reply, err := e.Issue(id, req)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := scep.ParsePKIMessage(reply)
	if err != nil {
		t.Fatal(err)
	}
	if err = rep.DecryptPKIEnvelope(device.cert, device.key); err != nil {
		t.Fatal(err)
	}
	issued := rep.Certificate
	if issued == nil || issued.IsCA || len(issued.DNSNames) != 0 || issued.CheckSignatureFrom(e.ca) != nil {
		t.Fatal("invalid issued certificate")
	}
	// Retries receive the same certificate, not a new identity.
	if _, err = e.Issue(id, req); err != nil {
		t.Fatal(err)
	}
	finalRequest := signTest(t, testIdentity{issued, device.key}, plist("<dict/>"))
	response, err := e.Profile(id, finalRequest)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.Profile(id, finalRequest)
	if err != nil || !bytes.Equal(response, again) {
		t.Fatal("final retry changed")
	}
	if e.sessions[id].password != nil {
		t.Fatal("plaintext retained after delivery")
	}
	outer := content(t, response)
	if bytes.Contains(outer, []byte("CalDAVPassword")) || bytes.Contains(outer, []byte("a&amp;b")) {
		t.Fatal("plaintext leaked")
	}
	doc := etree.NewDocument()
	if err = doc.ReadFromBytes(outer); err != nil {
		t.Fatal(err)
	}
	node := doc.FindElement("/plist/dict/data")
	if node == nil {
		t.Fatal("missing encrypted payload")
	}
	der, err := base64.StdEncoding.DecodeString(node.Text())
	if err != nil {
		t.Fatal(err)
	}
	enc, err := pkcs7.Parse(der)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := enc.Decrypt(issued, device.key)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(plain, []byte("<plist version=\"1.0\"><array>")) || !bytes.Contains(plain, []byte("CalDAVPassword")) || !bytes.Contains(plain, []byte("CardDAVPassword")) || !bytes.Contains(plain, []byte("password&lt;&amp;&gt;")) {
		t.Fatalf("incorrect encrypted content: %s", plain)
	}
	// Verify CMS interoperability using a separate implementation.
	if _, err := exec.LookPath("openssl"); err == nil {
		dir := t.TempDir()
		files := map[string][]byte{
			"signed.der": response, "encrypted.der": der,
			"cert.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: issued.Raw}),
			"key.pem":  pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(device.key)}),
		}
		for name, b := range files {
			if err := os.WriteFile(filepath.Join(dir, name), b, 0600); err != nil {
				t.Fatal(err)
			}
		}
		verify := exec.Command("openssl", "cms", "-verify", "-binary", "-inform", "DER", "-in", filepath.Join(dir, "signed.der"), "-noverify")
		if out, err := verify.Output(); err != nil || !bytes.Equal(out, outer) {
			t.Fatalf("OpenSSL signature verification failed: %v", err)
		}
		decrypt := exec.Command("openssl", "cms", "-decrypt", "-binary", "-inform", "DER", "-in", filepath.Join(dir, "encrypted.der"), "-recip", filepath.Join(dir, "cert.pem"), "-inkey", filepath.Join(dir, "key.pem"))
		if out, err := decrypt.Output(); err != nil || !bytes.Equal(out, plain) {
			t.Fatalf("OpenSSL decryption failed: %v", err)
		}
	}
	stranger := identity(t)
	if _, err = enc.Decrypt(stranger.cert, stranger.key); err == nil {
		t.Fatal("another identity decrypted profile")
	}
	// Apple's plist parser independently validates every plaintext plist shape.
	if _, err := exec.LookPath("plutil"); err == nil {
		for n, b := range [][]byte{content(t, initial), content(t, boot), outer, plain} {
			name := filepath.Join(t.TempDir(), string(rune('a'+n))+".plist")
			if err := os.WriteFile(name, b, 0600); err != nil {
				t.Fatal(err)
			}
			if out, err := exec.Command("plutil", "-lint", name).CombinedOutput(); err != nil {
				t.Fatalf("plist validation: %s", out)
			}
		}
	}
}
func TestEnrollmentRejectsWrongIdentityChallengeAndReplay(t *testing.T) {
	var e Enrollments
	id := startTest(t, &e)
	a := identity(t)
	b := identity(t)
	if _, err := e.Profile(id, signTest(t, a, plist("<dict>"+field("CHALLENGE", "wrong")+"</dict>"))); err == nil {
		t.Fatal("wrong challenge accepted")
	}
	bootstrap(t, &e, id, a)
	if _, err := e.Profile(id, signTest(t, b, plist("<dict>"+field("CHALLENGE", id)+"</dict>"))); err == nil {
		t.Fatal("bootstrap identity replaced")
	}
	bad := issueRequest(t, &e, id, a, "wrong")
	if _, err := e.Issue(id, bad); err == nil {
		t.Fatal("wrong SCEP challenge accepted")
	}
	req := issueRequest(t, &e, id, a, e.sessions[id].challenge)
	if _, err := e.Issue(id, req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Issue(id, issueRequest(t, &e, id, b, e.sessions[id].challenge)); err == nil {
		t.Fatal("issued second key")
	}
	if _, err := e.Profile(id, signTest(t, b, plist("<dict/>"))); err == nil {
		t.Fatal("wrong final signer accepted")
	}
	req[len(req)-1] ^= 1
	if _, err := e.Issue(id, req); err == nil {
		t.Fatal("tampered request accepted")
	}
	other := startTest(t, &e)
	if _, err := e.Profile(other, signTest(t, testIdentity{e.sessions[id].issued, a.key}, plist("<dict/>"))); err == nil {
		t.Fatal("cross-session final accepted")
	}
	e.sessions[id].until = time.Now().Add(-time.Second)
	if _, err := e.Initial(id); !errors.Is(err, ErrEnrollment) {
		t.Fatal("expired enrollment accepted")
	}
	if _, _, ok := e.Owner(id); ok {
		t.Fatal("expired owner retained")
	}
}
func TestEnrollmentLimitsAndCancel(t *testing.T) {
	var e Enrollments
	id := startTest(t, &e)
	startTest(t, &e)
	startTest(t, &e)
	_, err := e.Start(Options{ServerURL: "https://drive.example", UserID: "owner", Email: "a@example.test", InstallationID: "phone", Calendars: true}, "device", "secret")
	if !errors.Is(err, ErrEnrollmentLimit) {
		t.Fatalf("limit: %v", err)
	}
	s := e.sessions[id]
	secret := s.password
	e.Cancel(id)
	if s.password != nil || !bytes.Equal(secret, make([]byte, len(secret))) {
		t.Fatal("cancel retained password")
	}
	if _, err := e.CACert(id); err == nil {
		t.Fatal("canceled session available")
	}
}
func TestEnrollmentRejectsDuplicateChallenge(t *testing.T) {
	if _, err := profileChallenge(plist("<dict>" + strings.Repeat(field("CHALLENGE", "x"), 2) + "</dict>")); err == nil {
		t.Fatal("duplicate challenge accepted")
	}
}
