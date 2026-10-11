package contracts

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/eugenioenko/goalchemy/internal/testutil"
)

type tlsIdentity struct {
	cert    *x509.Certificate
	key     any
	certPEM string
	keyPEM  string
}

// issueTLS creates a CA (parent nil), server or client certificate. keyType
// selects the private key PEM form: "pkcs8", "pkcs1" (RSA) or "sec1" (EC).
func issueTLS(t *testing.T, name string, parent *tlsIdentity, server bool, keyType string) *tlsIdentity {
	t.Helper()
	var key any
	var pub any
	if keyType == "pkcs1" {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	} else {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		key, pub = k, &k.PublicKey
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour)}
	switch {
	case parent == nil:
		tmpl.IsCA, tmpl.BasicConstraintsValid, tmpl.KeyUsage = true, true, x509.KeyUsageCertSign|x509.KeyUsageDigitalSignature
	case server:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		tmpl.IPAddresses, tmpl.DNSNames = []net.IP{net.ParseIP("127.0.0.1")}, []string{"localhost"}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	default:
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, pub, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	var block *pem.Block
	switch keyType {
	case "pkcs1":
		block = &pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key.(*rsa.PrivateKey))}
	case "sec1":
		b, _ := x509.MarshalECPrivateKey(key.(*ecdsa.PrivateKey))
		block = &pem.Block{Type: "EC PRIVATE KEY", Bytes: b}
	default:
		b, _ := x509.MarshalPKCS8PrivateKey(key)
		block = &pem.Block{Type: "PRIVATE KEY", Bytes: b}
	}
	return &tlsIdentity{cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), string(pem.EncodeToMemory(block))}
}

// TestHTTPTLSOptions checks client certificates, extra root CAs and explicit
// insecure verification of lib/http.DoWith on every target against local
// servers, comparing with native Go.
func TestHTTPTLSOptions(t *testing.T) {
	ca := issueTLS(t, "goalchemy test CA", nil, false, "pkcs8")
	other := issueTLS(t, "other CA", nil, false, "pkcs8")
	serverID := issueTLS(t, "server", ca, true, "pkcs8")
	clients := map[string]*tlsIdentity{
		"pkcs8": issueTLS(t, "client-pkcs8", ca, false, "pkcs8"),
		"pkcs1": issueTLS(t, "client-pkcs1", ca, false, "pkcs1"),
		"sec1":  issueTLS(t, "client-sec1", ca, false, "sec1"),
	}
	stranger := issueTLS(t, "stranger", other, false, "pkcs8")
	pair, err := tls.X509KeyPair([]byte(serverID.certPEM), []byte(serverID.keyPEM))
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca.cert)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := "anonymous"
		if len(r.TLS.PeerCertificates) > 0 {
			name = r.TLS.PeerCertificates[0].Subject.CommonName
		}
		w.Write([]byte(name))
	})
	start := func(auth tls.ClientAuthType) *httptest.Server {
		s := httptest.NewUnstartedServer(handler)
		s.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientCAs: pool, ClientAuth: auth}
		s.StartTLS()
		return s
	}
	optional := start(tls.VerifyClientCertIfGiven)
	defer optional.Close()
	required := start(tls.RequireAndVerifyClientCert)
	defer required.Close()
	q := strconv.Quote
	program := fmt.Sprintf(`package main

import (
	"github.com/eugenioenko/goalchemy/lib/context"
	"github.com/eugenioenko/goalchemy/lib/http"
)

const optional = %s
const required = %s
const ca = %s
const other = %s
const stranger = %s
const strangerKey = %s

func show(name, url string, options []string) {
	status, _, body, err := http.DoWith(context.Background(), "GET", url, nil, nil, 1024, 10000, options)
	if err != nil {
		println(name, "error", status, body == nil)
		return
	}
	println(name, status, string(body))
}

func main() {
	show("default", optional, nil)
	show("explicit-false", optional, []string{http.InsecureSkipVerify, "false"})
	show("root-cas", optional, []string{http.RootCAs, ca})
	show("insecure", optional, []string{http.InsecureSkipVerify, "true"})
	show("other-ca", optional, []string{http.RootCAs, other})
	show("mtls-none", required, []string{http.RootCAs, ca})
	show("mtls-stranger", required, []string{http.RootCAs, ca, http.ClientCert, stranger, http.ClientKey, strangerKey})
	show("mtls-pkcs8", required, []string{http.RootCAs, ca, http.ClientCert, %s, http.ClientKey, %s})
	show("mtls-pkcs1", required, []string{http.RootCAs, ca, http.ClientCert, %s, http.ClientKey, %s})
	show("mtls-sec1", required, []string{http.RootCAs, ca, http.ClientCert, %s, http.ClientKey, %s})
	show("mtls-insecure", required, []string{http.InsecureSkipVerify, "true", http.ClientCert, %s, http.ClientKey, %s})
	show("odd", optional, []string{http.RootCAs})
	show("unknown", optional, []string{"proxy", "x"})
	show("repeated", optional, []string{http.RootCAs, ca, http.RootCAs, ca})
	show("cert-only", optional, []string{http.ClientCert, stranger})
	show("bad-insecure", optional, []string{http.InsecureSkipVerify, "yes"})
	show("bad-root", optional, []string{http.RootCAs, "not a certificate"})
	show("bad-pair", optional, []string{http.RootCAs, ca, http.ClientCert, "x", http.ClientKey, "y"})
}
`, q(optional.URL), q(required.URL), q(ca.certPEM), q(other.certPEM), q(stranger.certPEM), q(stranger.keyPEM),
		q(clients["pkcs8"].certPEM), q(clients["pkcs8"].keyPEM), q(clients["pkcs1"].certPEM), q(clients["pkcs1"].keyPEM),
		q(clients["sec1"].certPEM), q(clients["sec1"].keyPEM), q(clients["pkcs8"].certPEM), q(clients["pkcs8"].keyPEM))
	runAcrossTargets(t, "httptlsprobe", program, []string{
		"default error 0 true", "explicit-false error 0 true", "root-cas 200 anonymous", "insecure 200 anonymous",
		"other-ca error 0 true", "mtls-none error 0 true", "mtls-stranger error 0 true", "mtls-pkcs8 200 client-pkcs8",
		"mtls-pkcs1 200 client-pkcs1", "mtls-sec1 200 client-sec1", "mtls-insecure 200 client-pkcs8",
	}, nil)
}

// runAcrossTargets compiles program with the cooperative gate for every
// target (or GOALCHEMY_TEST_TARGETS) and compares output with native Go,
// whose output must contain each of lines. expect, when set, adjusts the
// native output a target must produce.
func runAcrossTargets(t *testing.T, module, program string, lines []string, expect func(target string, want testutil.Observation) testutil.Observation) {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	for name, data := range map[string]string{
		"main.go": program,
		"go.mod":  fmt.Sprintf("module %s\n\ngo 1.25\nrequire github.com/eugenioenko/goalchemy v0.0.0\nreplace github.com/eugenioenko/goalchemy => %s\n", module, strconv.Quote(root)),
	} {
		if err := os.WriteFile(filepath.Join(source, name), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	native, err := testutil.Native(source, t.TempDir())
	if err != nil || native.Exit != 0 {
		t.Fatalf("native: %v\n%s", err, native)
	}
	want := testutil.Normalize(native)
	for _, line := range lines {
		if !strings.Contains(want.Stdout+want.Stderr, line+"\n") {
			t.Fatalf("native output lacks %q\n%s", line, want)
		}
	}
	targets := []string{"go", "typescript", "python", "java", "csharp", "rust", "c", "swift"}
	if only := os.Getenv("GOALCHEMY_TEST_TARGETS"); only != "" {
		targets = strings.Split(only, ",")
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			out := t.TempDir()
			if ds := testutil.CompileGate(source, target, out, "cooperative"); len(ds) > 0 {
				t.Fatal(ds)
			}
			got, err := testutil.Runners[target](out)
			if err != nil {
				t.Fatal(err)
			}
			want := want
			if expect != nil {
				want = expect(target, want)
			}
			if g := testutil.Normalize(got); g != want {
				t.Errorf("%s differs from Go\n=== go\n%s=== %s\n%s", target, want, target, g)
			}
		})
	}
}
