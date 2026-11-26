//go:build vm

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bubbleptr/vpsgogo/packages/core/notify"
)

// webhook records the notices the agent posts. It listens on loopback only;
// lima forwards the VM's host.lima.internal to it.
type webhook struct {
	port  int
	caPEM string
	mu    sync.Mutex
	got   []notify.Message
	query string
}

func (w *webhook) messages() []notify.Message {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]notify.Message(nil), w.got...)
}

func (w *webhook) lastQuery() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.query
}

func startWebhook(t *testing.T) *webhook {
	t.Helper()
	ca, caKey := newCert(t, nil, nil, true)
	leaf, leafKey := newCert(t, ca, caKey, false)
	w := &webhook{caPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.Raw}))}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	w.port = ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{
		Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var m notify.Message
			if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
				http.Error(rw, err.Error(), http.StatusBadRequest)
				return
			}
			w.mu.Lock()
			w.got = append(w.got, m)
			w.query = r.URL.RawQuery
			w.mu.Unlock()
		}),
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{leaf.Raw}, PrivateKey: leafKey}}},
	}
	go srv.ServeTLS(ln, "", "")
	t.Cleanup(func() { srv.Close() })
	return w
}

// newCert makes a CA (parent nil) or a leaf for host.lima.internal signed by parent.
func newCert(t *testing.T, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: "vpsgogo e2e"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	if isCA {
		tmpl.IsCA, tmpl.BasicConstraintsValid = true, true
		tmpl.KeyUsage = x509.KeyUsageCertSign
		parent, parentKey = tmpl, key
	} else {
		tmpl.DNSNames = []string{"host.lima.internal"}
		tmpl.KeyUsage = x509.KeyUsageDigitalSignature
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key
}
