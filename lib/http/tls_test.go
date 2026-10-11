package http

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type testCert struct {
	cert    *x509.Certificate
	key     *ecdsa.PrivateKey
	certPEM string
	keyPEM  string
}

func issue(t *testing.T, name string, parent *testCert, server bool) *testCert {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	if parent == nil {
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature
	} else if server {
		tmpl.ExtKeyUsage, tmpl.IPAddresses, tmpl.DNSNames = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, []net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
	} else {
		tmpl.ExtKeyUsage, tmpl.KeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, x509.KeyUsageDigitalSignature
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pkcs8, _ := x509.MarshalPKCS8PrivateKey(key)
	return &testCert{cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}))}
}

func TestTLSOptions(t *testing.T) {
	ca := issue(t, "test CA", nil, false)
	other := issue(t, "other CA", nil, false)
	serverCert := issue(t, "server", ca, true)
	client := issue(t, "client", ca, false)
	stranger := issue(t, "stranger", other, false)
	pair, err := tls.X509KeyPair([]byte(serverCert.certPEM), []byte(serverCert.keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	clients := x509.NewCertPool()
	clients.AddCert(ca.cert)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.TLS.PeerCertificates) > 0 {
			w.Write([]byte(r.TLS.PeerCertificates[0].Subject.CommonName))
		}
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: clients, ClientAuth: tls.VerifyClientCertIfGiven}
	server.StartTLS()
	defer server.Close()
	mtls := httptest.NewUnstartedServer(server.Config.Handler)
	mtls.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: clients, ClientAuth: tls.RequireAndVerifyClientCert}
	mtls.StartTLS()
	defer mtls.Close()
	do := func(url string, options ...string) (string, error) {
		status, _, body, err := DoWith(context.Background(), "GET", url, nil, nil, 100, 5000, options)
		if err == nil && status != 200 {
			t.Fatal(status)
		}
		return string(body), err
	}
	if _, err := do(server.URL); err == nil {
		t.Fatal("untrusted server accepted by default")
	}
	if _, err := do(server.URL, InsecureSkipVerify, "false"); err == nil {
		t.Fatal("untrusted server accepted with explicit false")
	}
	if body, err := do(server.URL, RootCAs, ca.certPEM); err != nil || body != "" {
		t.Fatal(body, err)
	}
	if body, err := do(server.URL, InsecureSkipVerify, "true"); err != nil || body != "" {
		t.Fatal(body, err)
	}
	if _, err := do(server.URL, RootCAs, other.certPEM); err == nil {
		t.Fatal("wrong CA accepted")
	}
	if _, err := do(mtls.URL, RootCAs, ca.certPEM); err == nil {
		t.Fatal("mTLS without client certificate")
	}
	if _, err := do(mtls.URL, RootCAs, ca.certPEM, ClientCert, stranger.certPEM, ClientKey, stranger.keyPEM); err == nil {
		t.Fatal("mTLS with untrusted client certificate")
	}
	if body, err := do(mtls.URL, RootCAs, ca.certPEM, ClientCert, client.certPEM, ClientKey, client.keyPEM); err != nil || body != "client" {
		t.Fatal(body, err)
	}
	for _, options := range [][]string{
		{RootCAs},
		{"proxy", "x"},
		{RootCAs, ca.certPEM, RootCAs, ca.certPEM},
		{ClientCert, client.certPEM},
		{ClientKey, client.keyPEM},
		{InsecureSkipVerify, "yes"},
	} {
		if _, err := do(server.URL, options...); err != invalid {
			t.Fatal(options, err)
		}
	}
	for _, options := range [][]string{
		{RootCAs, "not a certificate"},
		{ClientCert, client.certPEM, ClientKey, stranger.keyPEM},
		{ClientCert, "x", ClientKey, "y"},
	} {
		if _, err := do(server.URL, options...); err != badTLS {
			t.Fatal(options, err)
		}
	}
}
