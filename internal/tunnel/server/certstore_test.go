package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"sync"
	"testing"
	"time"
)

func issue(t *testing.T, notAfter time.Time, names ...string) (certPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("serial: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: names[0]},
		DNSNames:              names,
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func TestCertStoreMatchesSingleLabelWildcards(t *testing.T) {
	store := NewCertStore()

	certPEM, keyPEM := issue(t, time.Now().Add(24*time.Hour), "aaa.com", "*.aaa.com")
	if _, err := store.Install(certPEM, keyPEM); err != nil {
		t.Fatalf("Install: %v", err)
	}

	serves := []string{"aaa.com", "www.aaa.com", "anything.aaa.com"}
	for _, host := range serves {
		if !store.Has(host) {
			t.Errorf("%s should be served", host)
		}
	}

	refuses := []string{"x.bb.aaa.com", "a.b.c.aaa.com", "other.com", "aaa.com.evil.com"}
	for _, host := range refuses {
		if store.Has(host) {
			t.Errorf("%s must NOT be served — a wildcard covers one label", host)
		}
	}
}

func TestCertStoreRejectsBadMaterial(t *testing.T) {
	store := NewCertStore()

	if _, err := store.Install([]byte("not a certificate"), []byte("nor a key")); err == nil {
		t.Error("garbage should be refused")
	}

	certA, _ := issue(t, time.Now().Add(time.Hour), "a.com")
	_, keyB := issue(t, time.Now().Add(time.Hour), "b.com")
	if _, err := store.Install(certA, keyB); err == nil {
		t.Error("a certificate and an unrelated key should be refused")
	}

	expiredCert, expiredKey := issue(t, time.Now().Add(-time.Hour), "old.com")
	if _, err := store.Install(expiredCert, expiredKey); err == nil {
		t.Error("an expired certificate should be refused")
	}

	if store.Len() != 0 {
		t.Errorf("store holds %d entries after only failures", store.Len())
	}
}

func TestCertStoreRequiresSNI(t *testing.T) {
	store := NewCertStore()
	certPEM, keyPEM := issue(t, time.Now().Add(time.Hour), "aaa.com")
	store.Install(certPEM, keyPEM)

	if _, err := store.GetCertificate(&tls.ClientHelloInfo{}); err == nil {
		t.Error("a hello without SNI cannot be answered and should error")
	}
}

func TestCertRotationDoesNotDisturbLiveConnections(t *testing.T) {
	store := NewCertStore()

	firstCert, firstKey := issue(t, time.Now().Add(24*time.Hour), "rotate.test")
	if _, err := store.Install(firstCert, firstKey); err != nil {
		t.Fatalf("install first: %v", err)
	}

	before, err := store.GetCertificate(&tls.ClientHelloInfo{ServerName: "rotate.test"})
	if err != nil {
		t.Fatalf("first lookup: %v", err)
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}

				if _, err := store.GetCertificate(
					&tls.ClientHelloInfo{ServerName: "rotate.test"}); err != nil {
					t.Errorf("lookup failed during rotation: %v", err)
					return
				}
			}
		}()
	}

	for range 50 {
		cert, key := issue(t, time.Now().Add(48*time.Hour), "rotate.test")
		if _, err := store.Install(cert, key); err != nil {
			t.Errorf("rotate: %v", err)
			break
		}
	}

	close(stop)
	wg.Wait()

	after, err := store.GetCertificate(&tls.ClientHelloInfo{ServerName: "rotate.test"})
	if err != nil {
		t.Fatalf("final lookup: %v", err)
	}

	if before.Leaf.SerialNumber.Cmp(after.Leaf.SerialNumber) == 0 {
		t.Error("the certificate did not change; rotation was a no-op")
	}
	if store.Len() != 1 {
		t.Errorf("store holds %d patterns after rotating one name, want 1", store.Len())
	}
}

func TestCertStoreReplacesRatherThanAccumulates(t *testing.T) {
	store := NewCertStore()

	for range 3 {
		cert, key := issue(t, time.Now().Add(time.Hour), "same.test", "*.same.test")
		if _, err := store.Install(cert, key); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}

	if store.Len() != 2 {
		t.Errorf("store holds %d patterns, want 2", store.Len())
	}
	if entries := store.Entries(); len(entries) != 2 {
		t.Errorf("Entries returned %d, want 2", len(entries))
	}
}

func TestCertStoreServesSeveralIndependentCertificates(t *testing.T) {
	store := NewCertStore()

	for _, name := range []string{"one.test", "two.test"} {
		cert, key := issue(t, time.Now().Add(time.Hour), name)
		if _, err := store.Install(cert, key); err != nil {
			t.Fatalf("Install %s: %v", name, err)
		}
	}

	for _, name := range []string{"one.test", "two.test"} {
		got, err := store.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatalf("lookup %s: %v", name, err)
		}
		if got.Leaf.Subject.CommonName != name {
			t.Errorf("%s got the certificate for %s", name, got.Leaf.Subject.CommonName)
		}
	}
}
