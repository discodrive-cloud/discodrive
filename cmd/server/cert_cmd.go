package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// runGenCert writes a self-signed TLS pair for nginx on first start: `server gen-cert
// [-dir /certs] [-host a.example.com,localhost]`. An existing pair is never touched, so
// real certificates dropped into the directory always win.
func runGenCert(args []string, out io.Writer, now time.Time) int {
	flags := flag.NewFlagSet("gen-cert", flag.ContinueOnError)
	flags.SetOutput(out)
	dir := flags.String("dir", "/certs", "directory for dev.pem and dev-key.pem")
	hosts := flags.String("host", "localhost", "comma-separated DNS names or IPs; the first one becomes the CN")
	days := flags.Int("days", 825, "validity in days")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	certPath, keyPath := filepath.Join(*dir, "dev.pem"), filepath.Join(*dir, "dev-key.pem")
	certOK, keyOK := exists(certPath), exists(keyPath)
	switch {
	case certOK && keyOK:
		fmt.Fprintf(out, "TLS certificate already present in %s, leaving it\n", *dir)
		return 0
	case certOK || keyOK:
		fmt.Fprintf(out, "only one of dev.pem / dev-key.pem exists in %s; remove it or add its pair\n", *dir)
		return 1
	}

	var names []string
	var ips []net.IP
	for h := range strings.SplitSeq(*hosts, ",") {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			ips = append(ips, ip)
		} else {
			names = append(names, h)
		}
	}
	cn := strings.TrimSpace(strings.Split(*hosts, ",")[0])

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		fmt.Fprintf(out, "generating key: %v\n", err)
		return 1
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		fmt.Fprintf(out, "generating serial: %v\n", err)
		return 1
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"DiscoDrive self-signed"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(0, 0, *days),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              names,
		IPAddresses:           ips,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		fmt.Fprintf(out, "creating certificate: %v\n", err)
		return 1
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		fmt.Fprintf(out, "encoding key: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintf(out, "creating %s: %v\n", *dir, err)
		return 1
	}
	// Key first: a certificate without its key would block the next run.
	if err := writeAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		fmt.Fprintf(out, "writing key: %v\n", err)
		return 1
	}
	if err := writeAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		os.Remove(keyPath)
		fmt.Fprintf(out, "writing certificate: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "wrote a self-signed certificate for %s to %s (valid %d days); replace dev.pem/dev-key.pem with real ones any time\n",
		strings.Join(append(names, ipStrings(ips)...), ", "), *dir, *days)
	return 0
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return !errors.Is(err, fs.ErrNotExist)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".gen-cert-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func ipStrings(ips []net.IP) []string {
	s := make([]string, len(ips))
	for i, ip := range ips {
		s[i] = ip.String()
	}
	return s
}
