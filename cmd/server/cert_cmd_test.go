package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenCertCreatesUsablePair(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	now := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	if code := runGenCert([]string{"-dir", dir, "-host", "cloud.example.com, localhost,127.0.0.1"}, &out, now); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}

	certPath, keyPath := filepath.Join(dir, "dev.pem"), filepath.Join(dir, "dev-key.pem")
	if _, err := tls.LoadX509KeyPair(certPath, keyPath); err != nil {
		t.Fatalf("pair is not usable by TLS: %v", err)
	}
	if fi, _ := os.Stat(keyPath); fi.Mode().Perm() != 0o600 {
		t.Fatalf("key mode %v, want 0600", fi.Mode().Perm())
	}

	raw, _ := os.ReadFile(certPath)
	block, _ := pem.Decode(raw)
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "cloud.example.com" {
		t.Fatalf("CN %q", cert.Subject.CommonName)
	}
	if err := cert.VerifyHostname("cloud.example.com"); err != nil {
		t.Fatal(err)
	}
	if err := cert.VerifyHostname("localhost"); err != nil {
		t.Fatal(err)
	}
	if len(cert.IPAddresses) != 1 || !cert.IPAddresses[0].Equal(net.ParseIP("127.0.0.1")) {
		t.Fatalf("IP SANs %v", cert.IPAddresses)
	}
	if want := now.AddDate(0, 0, 825); !cert.NotAfter.Equal(want) {
		t.Fatalf("NotAfter %v, want %v", cert.NotAfter, want)
	}
}

func TestGenCertKeepsExistingPair(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"dev.pem", "dev-key.pem"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("real "+name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if code := runGenCert([]string{"-dir", dir}, &out, time.Now()); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "dev.pem")); string(got) != "real dev.pem" {
		t.Fatalf("existing certificate was replaced: %q", got)
	}
}

func TestGenCertRefusesHalfPair(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dev.pem"), []byte("orphan"), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := runGenCert([]string{"-dir", dir}, &out, time.Now()); code == 0 {
		t.Fatal("generated a key next to a certificate it doesn't belong to")
	}
	if _, err := os.Stat(filepath.Join(dir, "dev-key.pem")); !os.IsNotExist(err) {
		t.Fatalf("key written: %v", err)
	}
}
