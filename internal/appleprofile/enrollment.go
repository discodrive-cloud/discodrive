package appleprofile

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" // Apple's SCEP CAFingerprint uses SHA-1, over authenticated HTTPS.
	"crypto/subtle"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/beevik/etree"
	"github.com/smallstep/pkcs7"
	"github.com/smallstep/scep"
)

// The library selects the CMS cipher globally. Set it once at package startup,
// never per request. AES-CBC is required for Apple CMS interoperability; all
// outgoing profiles are also CMS signed and transported over HTTPS.
func init() {
	pkcs7.ContentEncryptionAlgorithm = pkcs7.EncryptionAlgorithmAES256CBC
	if err := pkcs7.SetDefaultDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256); err != nil {
		panic(err)
	}
}

var ErrEnrollment = errors.New("invalid or expired enrollment")
var ErrEnrollmentLimit = errors.New("too many pending enrollments")

// Enrollments is an opt-in OTA experiment, not an MDM service. Its ephemeral CA
// is only used to deliver account credentials; no CA root or MDM payload is
// installed. Restarting the server invalidates pending sessions, not accounts.
type Enrollments struct {
	mu       sync.Mutex
	sessions map[string]*enrollment
	ca       *x509.Certificate
	key      *rsa.PrivateKey
}
type enrollment struct {
	options         Options
	deviceID        string
	password        []byte
	until           time.Time
	challenge       string
	bootstrapSigner []byte
	issued          *x509.Certificate
	final           []byte
}

func token() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (e *Enrollments) Start(o Options, deviceID, password string) (string, error) {
	if _, err := Build(o); err != nil {
		return "", err
	}
	if deviceID == "" || password == "" || len(password) > 1024 {
		return "", ErrEnrollment
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.sessions == nil {
		e.sessions = make(map[string]*enrollment)
	}
	count := 0
	for k, s := range e.sessions {
		if !time.Now().Before(s.until) {
			e.remove(k)
			continue
		}
		if s.options.UserID == o.UserID {
			count++
		}
	}
	if len(e.sessions) >= 64 || count >= 3 {
		return "", ErrEnrollmentLimit
	}
	if e.ca == nil {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return "", err
		}
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return "", err
		}
		tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DiscoDrive enrollment"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
		der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
		if err != nil {
			return "", err
		}
		ca, err := x509.ParseCertificate(der)
		if err != nil {
			return "", err
		}
		e.ca, e.key = ca, key
	}
	id, err := token()
	if err != nil {
		return "", err
	}
	challenge, err := token()
	if err != nil {
		return "", err
	}
	s := &enrollment{options: o, deviceID: deviceID, password: []byte(password), until: time.Now().Add(Lifetime), challenge: challenge}
	e.sessions[id] = s
	// Expire even an abandoned session with no subsequent HTTP requests.
	time.AfterFunc(Lifetime, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.sessions[id] == s {
			e.remove(id)
		}
	})
	return id, nil
}
func (e *Enrollments) remove(id string) {
	if s := e.sessions[id]; s != nil {
		clear(s.password)
		s.password = nil
		s.challenge = ""
		delete(e.sessions, id)
	}
}
func (e *Enrollments) session(id string) (*enrollment, error) {
	s := e.sessions[id]
	if s == nil {
		return nil, ErrEnrollment
	}
	if !time.Now().Before(s.until) {
		e.remove(id)
		return nil, ErrEnrollment
	}
	return s, nil
}

// Owner lets the HTTP layer recheck account/device revocation before each step.
func (e *Enrollments) Owner(id string) (Options, string, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.session(id)
	if err != nil {
		return Options{}, "", false
	}
	return s.options, s.deviceID, true
}
func (e *Enrollments) Cancel(id string) { e.mu.Lock(); defer e.mu.Unlock(); e.remove(id) }

func xmlText(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
func field(k, v string) string { return "<key>" + k + "</key><string>" + xmlText(v) + "</string>" }
func plist(content string) []byte {
	return []byte(xml.Header + `<plist version="1.0">` + content + `</plist>`)
}
func wrap(id, kind, content string) []byte {
	return plist("<dict>" + field("PayloadIdentifier", id) + field("PayloadUUID", uuid(id)) + field("PayloadType", kind) + field("PayloadDisplayName", "DiscoDrive") + "<key>PayloadVersion</key><integer>1</integer>" + content + "</dict>")
}
func (e *Enrollments) sign(data []byte) ([]byte, error) {
	sd, err := pkcs7.NewSignedData(data)
	if err != nil {
		return nil, err
	}
	sd.SetDigestAlgorithm(pkcs7.OIDDigestAlgorithmSHA256)
	if err := sd.AddSigner(e.ca, e.key, pkcs7.SignerInfoConfig{}); err != nil {
		return nil, err
	}
	return sd.Finish()
}
func (e *Enrollments) Initial(id string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.session(id)
	if err != nil {
		return nil, err
	}
	return e.sign(wrap("org.discodrive.enrollment."+id, "Profile Service", "<key>PayloadContent</key><dict>"+field("URL", s.options.ServerURL+"/apple-enrollment/"+id+"/profile")+field("Challenge", id)+"<key>DeviceAttributes</key><array><string>VERSION</string></array></dict>"))
}
func (e *Enrollments) identityProfile(id string, s *enrollment) ([]byte, error) {
	fingerprint := sha1.Sum(e.ca.Raw)
	contents := "<key>PayloadContent</key><dict>" + field("URL", s.options.ServerURL+"/apple-enrollment/"+id+"/scep") + field("Challenge", s.challenge) + field("Key Type", "RSA") + `<key>Keysize</key><integer>2048</integer><key>Key Usage</key><integer>5</integer><key>KeyIsExtractable</key><false/><key>AllowAllAppsAccess</key><false/><key>Subject</key><array><array><array><string>CN</string><string>DiscoDrive enrollment</string></array></array></array><key>CAFingerprint</key><data>` + base64.StdEncoding.EncodeToString(fingerprint[:]) + "</data></dict>"
	identity := wrap("org.discodrive.enrollment.identity."+id, "com.apple.security.scep", contents)
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(identity); err != nil {
		return nil, err
	}
	dictDoc := etree.NewDocument()
	dictDoc.SetRoot(doc.Root().SelectElement("dict").Copy())
	dict, err := dictDoc.WriteToString()
	if err != nil {
		return nil, err
	}
	return e.sign(wrap("org.discodrive.enrollment."+id, "Configuration", "<key>PayloadContent</key><array>"+dict+"</array>"))
}

func signedMessage(data []byte) (*pkcs7.PKCS7, *x509.Certificate, error) {
	if len(data) == 0 || len(data) > 128<<10 {
		return nil, nil, ErrEnrollment
	}
	p, err := pkcs7.Parse(data)
	if err != nil {
		return nil, nil, ErrEnrollment
	}
	signer := p.GetOnlySigner()
	if signer == nil || p.Verify() != nil {
		return nil, nil, ErrEnrollment
	}
	return p, signer, nil
}
func profileChallenge(data []byte) (string, error) {
	doc := etree.NewDocument()
	if err := doc.ReadFromBytes(data); err != nil {
		return "", ErrEnrollment
	}
	root := doc.Root()
	if root == nil || root.Tag != "plist" {
		return "", ErrEnrollment
	}
	dict := root.SelectElement("dict")
	if dict == nil {
		return "", ErrEnrollment
	}
	elements := dict.ChildElements()
	if len(elements)%2 != 0 {
		return "", ErrEnrollment
	}
	seen := map[string]bool{}
	challenge := ""
	for i := 0; i < len(elements); i += 2 {
		key := elements[i]
		value := elements[i+1]
		if key.Tag != "key" || seen[key.Text()] {
			return "", ErrEnrollment
		}
		seen[key.Text()] = true
		if key.Text() == "CHALLENGE" {
			if value.Tag != "string" {
				return "", ErrEnrollment
			}
			challenge = value.Text()
		}
	}
	return challenge, nil
}

// Profile accepts proof of possession, not a claim about Apple hardware identity.
// The random HTTPS bootstrap ticket authorizes this session. The first signature
// pins it to one requester; the final request must use the exact issued identity.
func (e *Enrollments) Profile(id string, data []byte) ([]byte, error) {
	p, signer, err := signedMessage(data)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.session(id)
	if err != nil {
		return nil, err
	}
	if s.issued != nil && bytes.Equal(signer.Raw, s.issued.Raw) {
		if time.Now().After(s.issued.NotAfter) {
			return nil, ErrEnrollment
		}
		if s.final != nil {
			return bytes.Clone(s.final), nil
		}
		encrypted, err := encryptedProfile(s.options, string(s.password), s.issued)
		if err != nil {
			return nil, err
		}
		result, err := e.sign(encrypted)
		if err != nil {
			return nil, err
		}
		s.final = result
		clear(s.password)
		s.password = nil
		return bytes.Clone(result), nil
	}
	challenge, err := profileChallenge(p.Content)
	if err != nil || subtle.ConstantTimeCompare([]byte(challenge), []byte(id)) != 1 {
		return nil, ErrEnrollment
	}
	if s.bootstrapSigner != nil && !bytes.Equal(s.bootstrapSigner, signer.Raw) {
		return nil, ErrEnrollment
	}
	if s.issued != nil {
		return nil, ErrEnrollment
	}
	s.bootstrapSigner = bytes.Clone(signer.Raw)
	return e.identityProfile(id, s)
}

func encryptedProfile(o Options, password string, recipient *x509.Certificate) ([]byte, error) {
	if password == "" {
		return nil, ErrEnrollment
	}
	plain, err := build(o, password)
	if err != nil {
		return nil, err
	}
	defer clear(plain)
	// Build emits exactly one top-level payload array. Serialize only that array,
	// as required by Apple's EncryptedPayloadContent format.
	marker := []byte("<key>PayloadContent</key>")
	start := bytes.Index(plain, marker)
	end := bytes.LastIndex(plain, []byte("</array>")) + len("</array>")
	if start < 0 || end <= start {
		return nil, ErrEnrollment
	}
	payload := plist(string(plain[start+len(marker) : end]))
	defer clear(payload)
	encrypted, err := pkcs7.Encrypt(payload, []*x509.Certificate{recipient})
	if err != nil {
		return nil, err
	}
	result := bytes.NewBuffer(bytes.Clone(plain[:start]))
	fmt.Fprintf(result, "<key>EncryptedPayloadContent</key><data>%s</data>", base64.StdEncoding.EncodeToString(encrypted))
	result.Write(plain[end:])
	return result.Bytes(), nil
}

func (e *Enrollments) CACert(id string) ([]byte, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.session(id); err != nil {
		return nil, err
	}
	return bytes.Clone(e.ca.Raw), nil
}
func (e *Enrollments) Issue(id string, data []byte) ([]byte, error) {
	_, signer, err := signedMessage(data)
	if err != nil {
		return nil, err
	}
	msg, err := scep.ParsePKIMessage(data)
	if err != nil || msg.MessageType != scep.PKCSReq || len(msg.SenderNonce) < 16 || len(msg.TransactionID) > 128 {
		return nil, ErrEnrollment
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	s, err := e.session(id)
	if err != nil || s.bootstrapSigner == nil {
		return nil, ErrEnrollment
	}
	if err := msg.DecryptPKIEnvelope(e.ca, e.key); err != nil || msg.CSRReqMessage == nil {
		return nil, ErrEnrollment
	}
	csr := msg.CSR
	if csr == nil || csr.CheckSignature() != nil || subtle.ConstantTimeCompare([]byte(msg.ChallengePassword), []byte(s.challenge)) != 1 {
		return nil, ErrEnrollment
	}
	pub, ok := csr.PublicKey.(*rsa.PublicKey)
	if !ok || pub.N.BitLen() < 2048 || pub.N.BitLen() > 4096 || !bytes.Equal(csr.RawSubjectPublicKeyInfo, signer.RawSubjectPublicKeyInfo) {
		return nil, ErrEnrollment
	}
	if s.issued != nil && !bytes.Equal(s.issued.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) {
		return nil, ErrEnrollment
	}
	if s.issued == nil {
		serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		// Do not honor requested extensions, SANs, or CA privileges.
		tpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "DiscoDrive enrollment " + id[:12]}, NotBefore: time.Now().Add(-time.Minute), NotAfter: s.until.Add(time.Minute), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment}
		der, err := x509.CreateCertificate(rand.Reader, tpl, e.ca, pub, e.key)
		if err != nil {
			return nil, err
		}
		s.issued, err = x509.ParseCertificate(der)
		if err != nil {
			return nil, err
		}
	}
	response, err := msg.Success(e.ca, e.key, s.issued)
	if err != nil {
		return nil, err
	}
	return response.Raw, nil
}
