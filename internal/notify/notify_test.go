package notify

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"ticketd/internal/store"
)

func selfSignedPEM(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		DNSNames:     []string{"mail.internal"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestParseCA(t *testing.T) {
	pool, err := ParseCA("")
	if err != nil || pool != nil {
		t.Fatal("empty CA input must mean 'use embedded roots'")
	}
	if _, err := ParseCA("not a certificate"); err == nil {
		t.Fatal("garbage accepted")
	}
	pool, err = ParseCA(selfSignedPEM(t))
	if err != nil || pool == nil {
		t.Fatalf("valid PEM rejected: %v", err)
	}
}

func TestTLSConfigPolicy(t *testing.T) {
	// Default: verify against the system/embedded roots for the SMTP host.
	tc, err := tlsConfig(store.NotifySettings{SMTPHost: "mail.internal"})
	if err != nil {
		t.Fatal(err)
	}
	if tc.RootCAs != nil || tc.InsecureSkipVerify || tc.ServerName != "mail.internal" {
		t.Fatalf("unexpected defaults: %+v", tc)
	}

	// Custom CA pool.
	tc, err = tlsConfig(store.NotifySettings{SMTPHost: "mail.internal", SMTPCACert: selfSignedPEM(t)})
	if err != nil || tc.RootCAs == nil || tc.InsecureSkipVerify {
		t.Fatalf("custom CA not applied: %v", err)
	}

	// Explicit opt-out.
	tc, err = tlsConfig(store.NotifySettings{SMTPHost: "mail.internal", SMTPInsecureTLS: true})
	if err != nil || !tc.InsecureSkipVerify {
		t.Fatalf("insecure flag not honored: %v", err)
	}

	// Invalid CA PEM is an error, not a silent fallback.
	if _, err := tlsConfig(store.NotifySettings{SMTPHost: "h", SMTPCACert: "junk"}); err == nil {
		t.Fatal("invalid CA must error")
	}
}
