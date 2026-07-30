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
	"net"
	"testing"
	"time"
)

func TestTerminationOffersOnlyHTTP11(t *testing.T) {
	store := NewCertStore()
	chain, key := selfSigned(t, "*.example.com")
	if _, err := store.Install(chain, key); err != nil {
		t.Fatal(err)
	}

	config := store.TLSConfig()
	for _, proto := range config.NextProtos {
		if proto != "http/1.1" {
			t.Errorf("ALPN offers %q; a byte relay cannot promise it", proto)
		}
	}

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		conn := tls.Server(server, config)
		conn.HandshakeContext(t.Context())
		conn.Close()
	}()

	conn := tls.Client(client, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
		ServerName:         "www.example.com",
	})
	if err := conn.HandshakeContext(t.Context()); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if got := conn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", got)
	}
}

func selfSigned(t *testing.T, name string) (chainPEM, keyPEM []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		DNSNames:     []string{name},
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encodedKey, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encodedKey})
}
