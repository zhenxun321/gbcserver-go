package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

func validateWebListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("监听地址必须使用 IP:端口 格式: %w", err)
	}
	if net.ParseIP(host) == nil {
		return errors.New("监听地址必须使用 IP 字面值，可指定 0.0.0.0 监听所有网卡")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("监听端口必须在 1–65535 之间")
	}
	return nil
}

func serveWebListeners(ctx context.Context, handler http.Handler, httpAddress, httpsAddress string, certificate *tls.Certificate) error {
	return serveWebListenersWithReady(ctx, handler, httpAddress, httpsAddress, certificate, nil)
}

func serveWebListenersWithReady(ctx context.Context, handler http.Handler, httpAddress, httpsAddress string, certificate *tls.Certificate, ready chan<- string) error {
	httpListener, err := net.Listen("tcp", httpAddress)
	if err != nil {
		return fmt.Errorf("启动 HTTP listener 失败: %w", err)
	}
	servers := []*http.Server{newWebHTTPServer(httpAddress, handler)}
	listeners := []net.Listener{httpListener}
	if certificate != nil {
		httpsListener, err := net.Listen("tcp", httpsAddress)
		if err != nil {
			httpListener.Close()
			return fmt.Errorf("启动 HTTPS listener 失败；HTTP 也已取消: %w", err)
		}
		tlsListener := tls.NewListener(httpsListener, &tls.Config{
			Certificates: []tls.Certificate{*certificate},
			MinVersion:   tls.VersionTLS12,
		})
		servers = append(servers, newWebHTTPServer(httpsAddress, handler))
		listeners = append(listeners, tlsListener)
	}

	fmt.Fprintln(os.Stdout, "Web 服务已启动：HTTP://"+httpListener.Addr().String())
	if certificate != nil {
		fmt.Fprintln(os.Stdout, "Web 服务已启动：HTTPS://"+listeners[1].Addr().String())
	}
	results := make(chan error, len(servers))
	for index := range servers {
		server, listener := servers[index], listeners[index]
		go func() { results <- server.Serve(listener) }()
	}
	if ready != nil {
		ready <- "Web"
	}

	var serveErr error
	select {
	case <-ctx.Done():
	case serveErr = <-results:
		if serveErr == nil || errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = errors.New("listener 意外停止")
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var shutdownErrors []error
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil {
			shutdownErrors = append(shutdownErrors, err)
		}
	}
	if serveErr != nil {
		return errors.Join(serveErr, errors.Join(shutdownErrors...))
	}
	return errors.Join(shutdownErrors...)
}

func newWebHTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 * 1024,
	}
}
