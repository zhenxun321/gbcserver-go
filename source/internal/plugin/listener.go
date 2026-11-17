package plugin

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ListenerConfig 配置仅供内网访问的白名单与兑换接口。
type ListenerConfig struct {
	WhitelistAddress        string
	CouponAddress           string
	ExpectedServerID        string
	SignTimezoneOffsetHours int
	SignTimezoneExplicit    bool
}

// ValidateListenAddress 要求监听地址是服务端拥有的显式内网 IP。
func ValidateListenAddress(address string) error {
	host, portText, err := net.SplitHostPort(strings.TrimSpace(address))
	if err != nil {
		return fmt.Errorf("监听地址必须使用 IP:port 格式: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() || !isPrivateAddress(ip) {
		return errors.New("监听地址必须是显式 loopback、私有或 link-local IP；跨机器访问时请填服务端内网 IP")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("监听端口必须在 1–65535 之间")
	}
	return nil
}

// Serve 启动两个隔离的明文 HTTP listener，供游戏服通过内网直接访问。
func Serve(ctx context.Context, db *sql.DB, config ListenerConfig) error {
	return ServeWithReady(ctx, db, config, nil)
}

// ServeWithReady 在两个插件 listener 均已绑定后通知调用方。
func ServeWithReady(ctx context.Context, db *sql.DB, config ListenerConfig, ready chan<- string) error {
	if db == nil {
		return errors.New("plugin 数据库未初始化")
	}
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("plugin 数据库连接失败: %w", err)
	}
	if err := ValidateListenAddress(config.WhitelistAddress); err != nil {
		return fmt.Errorf("白名单监听地址无效: %w", err)
	}
	if err := ValidateListenAddress(config.CouponAddress); err != nil {
		return fmt.Errorf("兑换监听地址无效: %w", err)
	}
	if strings.EqualFold(config.WhitelistAddress, config.CouponAddress) {
		return errors.New("白名单和兑换码必须使用不同监听地址")
	}
	whitelistListener, err := net.Listen("tcp", config.WhitelistAddress)
	if err != nil {
		return fmt.Errorf("启动白名单 listener 失败: %w", err)
	}
	couponListener, err := net.Listen("tcp", config.CouponAddress)
	if err != nil {
		whitelistListener.Close()
		return fmt.Errorf("启动兑换码 listener 失败: %w", err)
	}

	service := NewSQLiteService(db)
	if config.SignTimezoneExplicit {
		service.LoginTimezoneOffsetHours = config.SignTimezoneOffsetHours
	}
	if err := service.ExpireSessionLeases(ctx); err != nil {
		whitelistListener.Close()
		couponListener.Close()
		return fmt.Errorf("启动时清理过期在线租约失败: %w", err)
	}
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
				if err := service.ExpireSessionLeases(cleanupCtx); err != nil {
					log.Printf("在线会话租约清理失败: %v", err)
				}
				cancel()
			}
		}
	}()
	handler := &Handler{
		Tokens:           NewTokenStore(db),
		Whitelist:        service,
		Coupons:          service,
		ExpectedServerID: strings.TrimSpace(config.ExpectedServerID),
	}
	servers := []*http.Server{
		{Addr: config.WhitelistAddress, Handler: NewWhitelistHTTPHandler(handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024},
		{Addr: config.CouponAddress, Handler: NewCouponHTTPHandler(handler), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024},
	}
	listeners := []net.Listener{whitelistListener, couponListener}
	log.Printf("白名单内网接口已启动：http://%s；兑换内网接口已启动：http://%s", whitelistListener.Addr(), couponListener.Addr())
	results := make(chan error, len(servers))
	for index := range servers {
		server := servers[index]
		listener := listeners[index]
		go func() {
			if serveErr := server.Serve(listener); !errors.Is(serveErr, http.ErrServerClosed) {
				results <- serveErr
				return
			}
			results <- nil
		}()
	}
	if ready != nil {
		ready <- "插件"
	}

	select {
	case <-ctx.Done():
	case err := <-results:
		if err != nil {
			for _, listener := range listeners {
				listener.Close()
			}
			return fmt.Errorf("plugin listener 异常退出: %w", err)
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
	return errors.Join(shutdownErrors...)
}
