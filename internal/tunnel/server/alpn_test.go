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

// TestTerminationOffersOnlyHTTP11 pins the ALPN list.
//
// Edge termination decrypts and then relays the plaintext to the tunnel
// untouched, so whatever protocol is negotiated here is spoken directly at the
// LAN service. Advertising h2 made a browser send HTTP/2 frames to an nginx
// that answered 421 Misdirected Request; the same request over HTTP/1.1
// returned 200. The visitor sees a broken site, and nothing in the proxy's own
// logs looks wrong.
func TestTerminationOffersOnlyHTTP11(t *testing.T) {
	store := NewCertStore()
	chain, key := selfSigned(t, "*.aiqno.com")
	if _, err := store.Install(chain, key); err != nil {
		t.Fatal(err)
	}

	config := store.TLSConfig()
	for _, proto := range config.NextProtos {
		if proto != "http/1.1" {
			t.Errorf("ALPN offers %q; a byte relay cannot promise it", proto)
		}
	}

	// Negotiate for real: a client that asks for h2 must be told http/1.1,
	// rather than the config merely listing it.
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
		ServerName:         "www.aiqno.com",
	})
	if err := conn.HandshakeContext(t.Context()); err != nil {
		t.Fatalf("handshake: %v", err)
	}

	if got := conn.ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Errorf("negotiated %q, want http/1.1", got)
	}
}

// selfSigned builds a certificate for name, in PEM.
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
