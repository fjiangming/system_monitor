package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestHTTPSRollbackPreservesActiveCredentialsAcrossRestart(t *testing.T) {
	s := testStore(t)
	root := s.Root
	private, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "monitor-test"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true, IsCA: true, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, &private.PublicKey, private)
	if e != nil {
		t.Fatal(e)
	}
	keyDER, e := x509.MarshalECPrivateKey(private)
	if e != nil {
		t.Fatal(e)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	key := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(cert))
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}, Timeout: time.Second}
	defer client.CloseIdleConnections()
	available, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := available.Addr().(*net.TCPAddr).Port
	available.Close()
	c, rev := s.Config()
	c.Service.ListenAddress = "127.0.0.1"
	c.Service.ListenPort = port
	c.Service.TLSMode = "builtin"
	c.Storage.DataBackup = false
	if e = s.SaveConfig(c, rev, map[string]string{"tlsCertificate": cert, "tlsPrivateKey": key}); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveActiveService(c.Service); e != nil {
		t.Fatal(e)
	}
	if e = s.Setup(s.SetupCode(), "admin", "a-long-test-password"); e != nil {
		t.Fatal(e)
	}
	token, csrf, _, e := s.Login("admin", "a-long-test-password", "127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	base := fmt.Sprintf("https://127.0.0.1:%d", port)
	start := func(store *Store) func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { defer close(done); done <- serveHub(ctx, store) }()
		stop := func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Error(err)
				}
			case <-time.After(5 * time.Second):
				t.Error("hub did not stop")
			}
		}
		t.Cleanup(stop)
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp, e := client.Get(base + "/healthz")
			if e == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return stop
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("HTTPS hub did not start")
		return stop
	}
	stop := start(s)
	blocked, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer blocked.Close()
	c, rev = s.Config()
	c.Service.ListenPort = blocked.Addr().(*net.TCPAddr).Port
	c.Service.TLSMode = "proxy"
	if e = s.SaveConfig(c, rev, nil, "tlsCertificate", "tlsPrivateKey"); e != nil {
		t.Fatal(e)
	}
	r, e := http.NewRequest("POST", base+"/api/runtime/apply", strings.NewReader("{}"))
	if e != nil {
		t.Fatal(e)
	}
	r.AddCookie(&http.Cookie{Name: "monitor_session", Value: token})
	r.Header.Set("X-CSRF-Token", csrf)
	resp, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatal(resp.StatusCode)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !strings.HasPrefix(s.meta("applyStatus"), "应用失败") {
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.HasPrefix(s.meta("applyStatus"), "应用失败") {
		t.Fatal("failed listener was not rolled back")
	}
	resp, e = client.Get(base + "/healthz")
	if e != nil {
		t.Fatal("rollback lost active HTTPS", e)
	}
	resp.Body.Close()
	stop()
	client.CloseIdleConnections()
	next, e := openStore(root)
	if e != nil {
		t.Fatal(e)
	}
	stop2 := start(next)
	stop2()
}
