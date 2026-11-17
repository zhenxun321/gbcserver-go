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
	"io"
	"math/big"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestValidateWebListenAddressAllowsAllInterfaces(t *testing.T) {
	for _, address := range []string{"0.0.0.0:80", "[::]:443", "127.0.0.1:18080"} {
		if err := validateWebListenAddress(address); err != nil {
			t.Errorf("应接受 Web 监听地址 %q: %v", address, err)
		}
	}
	for _, address := range []string{"localhost:80", "0.0.0.0:0", "127.0.0.1:65536"} {
		if err := validateWebListenAddress(address); err == nil {
			t.Errorf("应拒绝 Web 监听地址 %q", address)
		}
	}
}

func TestServeWebListenersRunsHTTPWithoutCertificate(t *testing.T) {
	httpAddress := reserveTCPAddress(t)
	httpsAddress := reserveTCPAddress(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := startTestWebListener(t, ctx, httpAddress, httpsAddress, nil)

	assertWebResponse(t, newTestHTTPClient(nil), "http://"+httpAddress)
	listener, err := net.Listen("tcp", httpsAddress)
	if err != nil {
		t.Fatalf("没有证书时不应绑定 HTTPS 地址 %s: %v", httpsAddress, err)
	}
	_ = listener.Close()

	cancel()
	assertWebListenerStopped(t, done)
}

func TestServeWebListenersRunsHTTPAndHTTPSWithCertificate(t *testing.T) {
	httpAddress := reserveTCPAddress(t)
	httpsAddress := reserveTCPAddress(t)
	certificate, roots := testWebCertificate(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := startTestWebListener(t, ctx, httpAddress, httpsAddress, &certificate)

	assertWebResponse(t, &http.Client{Timeout: time.Second}, "http://"+httpAddress)
	_, port, err := net.SplitHostPort(httpsAddress)
	if err != nil {
		t.Fatal(err)
	}
	client := newTestHTTPClient(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots})
	assertWebResponse(t, client, "https://localhost:"+port)

	cancel()
	assertWebListenerStopped(t, done)
}

func startTestWebListener(t *testing.T, ctx context.Context, httpAddress, httpsAddress string, certificate *tls.Certificate) <-chan error {
	t.Helper()
	done := make(chan error, 1)
	handler := http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("web-ready"))
	})
	go func() {
		done <- serveWebListeners(ctx, handler, httpAddress, httpsAddress, certificate)
	}()
	return done
}

func assertWebResponse(t *testing.T, client *http.Client, address string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(address)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && strings.TrimSpace(string(body)) == "web-ready" {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("Web listener 未响应: %s", address)
}

func newTestHTTPClient(tlsConfig *tls.Config) *http.Client {
	return &http.Client{
		Timeout: time.Second,
		Transport: &http.Transport{
			Proxy:           nil,
			TLSClientConfig: tlsConfig,
		},
	}
}

func assertWebListenerStopped(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Web listener 关闭失败: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Web listener 未在超时内关闭")
	}
}

func reserveTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func testWebCertificate(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "localhost"},
		DNSNames:              []string{"localhost"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	certificate, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	if len(certificate.Certificate) != 1 {
		t.Fatalf("测试证书链长度不正确: %d", len(certificate.Certificate))
	}
	return certificate, roots
}
