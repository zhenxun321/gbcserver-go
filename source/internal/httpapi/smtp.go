package httpapi

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPMailSender 是可替换的生产邮件发送器。密码只从运行时配置读取，不写入日志。
type SMTPMailSender struct {
	Host        string
	Port        int
	Username    string
	Password    string
	FromName    string
	FromAddress string
	Secure      bool
	Timeout     time.Duration
}

// Send 发送纯文本邮件；Secure=true 时使用 SMTPS，其他情况优先使用 STARTTLS。
func (s SMTPMailSender) Send(ctx context.Context, to, subject, body string) error {
	if strings.TrimSpace(s.Host) == "" || s.Port < 1 || s.Port > 65535 {
		return errors.New("SMTP 配置不完整")
	}
	if strings.TrimSpace(s.FromAddress) == "" || strings.TrimSpace(to) == "" {
		return errors.New("邮件地址不能为空")
	}
	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return context.DeadlineExceeded
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	address := net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
	dialer := &net.Dialer{Timeout: timeout}
	var conn net.Conn
	var err error
	if s.Secure {
		conn, err = tls.DialWithDialer(dialer, "tcp", address, &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器失败: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		return fmt.Errorf("创建 SMTP 客户端失败: %w", err)
	}
	defer client.Close()
	if hasStartTLS, _ := client.Extension("STARTTLS"); !s.Secure && hasStartTLS {
		if err := client.StartTLS(&tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("SMTP STARTTLS 失败: %w", err)
		}
	}
	if s.Username != "" {
		auth := smtp.PlainAuth("", s.Username, s.Password, s.Host)
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(s.FromAddress); err != nil {
		return fmt.Errorf("SMTP 发件人被拒绝: %w", err)
	}
	if err := client.Rcpt(to); err != nil {
		return fmt.Errorf("SMTP 收件人被拒绝: %w", err)
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP 开始写信失败: %w", err)
	}
	message := buildPlainMail(s.FromName, s.FromAddress, to, subject, body)
	if _, err := io.WriteString(writer, message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("SMTP 写信失败: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("SMTP 提交邮件失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("SMTP 结束会话失败: %w", err)
	}
	return nil
}

func buildPlainMail(fromName, from, to, subject, body string) string {
	fromHeader := from
	if strings.TrimSpace(fromName) != "" {
		fromHeader = fmt.Sprintf("=?UTF-8?B?%s?= <%s>", encodeHeader(fromName), from)
	}
	return "From: " + fromHeader + "\r\n" +
		"To: " + to + "\r\n" +
		"Subject: =?UTF-8?B?" + encodeHeader(subject) + "?=\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: text/plain; charset=UTF-8\r\n" +
		"Content-Transfer-Encoding: 8bit\r\n\r\n" + body + "\r\n"
}

func encodeHeader(value string) string {
	return base64Encode([]byte(value))
}

func base64Encode(value []byte) string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	var output strings.Builder
	for index := 0; index < len(value); index += 3 {
		remaining := len(value) - index
		first := value[index]
		second, third := byte(0), byte(0)
		if remaining > 1 {
			second = value[index+1]
		}
		if remaining > 2 {
			third = value[index+2]
		}
		output.WriteByte(alphabet[first>>2])
		output.WriteByte(alphabet[((first&0x03)<<4)|(second>>4)])
		if remaining > 1 {
			output.WriteByte(alphabet[((second&0x0f)<<2)|(third>>6)])
		} else {
			output.WriteString("=")
		}
		if remaining > 2 {
			output.WriteByte(alphabet[third&0x3f])
		} else {
			output.WriteString("=")
		}
	}
	return output.String()
}
