package verification

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

var (
	// ErrInvalidSMTPOptions 表示 SMTP 地址、发件人或超时不适合创建发送器。
	ErrInvalidSMTPOptions = errors.New("SMTP sender options are invalid")

	// ErrSMTPEmailChannelRequired 表示 SMTP Sender 收到了短信等非邮件挑战。
	ErrSMTPEmailChannelRequired = errors.New("SMTP sender only supports email verification")
)

// SMTPOptions 保存连接本地 Mailpit 所需的无认证 SMTP 配置。
// 该适配器只面向开发环境，不包含生产邮件服务所需的 TLS 和认证信息。
type SMTPOptions struct {
	Host        string
	Port        int
	FromAddress string
	FromName    string
	Timeout     time.Duration
}

// smtpDeliverFunc 把已经构造好的 RFC 5322 邮件交给 SMTP 服务。
// 函数字段允许单元测试替换网络层，同时继续验证真实邮件内容。
type smtpDeliverFunc func(
	ctx context.Context,
	address string,
	host string,
	envelopeFrom string,
	recipients []string,
	message []byte,
	timeout time.Duration,
) error

// SMTPSender 通过本地无认证 SMTP 服务发送验证码邮件。
type SMTPSender struct {
	host        string
	port        int
	fromAddress string
	fromHeader  string
	timeout     time.Duration
	deliver     smtpDeliverFunc
}

var _ verificationapplication.Sender = (*SMTPSender)(nil)

// NewSMTPSender 校验配置并创建 Mailpit SMTP 发送适配器。
func NewSMTPSender(options SMTPOptions) (*SMTPSender, error) {
	host := strings.TrimSpace(options.Host)
	if host == "" || strings.ContainsAny(host, "\r\n\t /\\") {
		return nil, fmt.Errorf("%w: invalid host", ErrInvalidSMTPOptions)
	}
	if options.Port < 1 || options.Port > 65535 {
		return nil, fmt.Errorf("%w: invalid port", ErrInvalidSMTPOptions)
	}

	fromAddress := strings.TrimSpace(options.FromAddress)
	parsedFromAddress, err := mail.ParseAddress(fromAddress)
	if err != nil || parsedFromAddress.Address != fromAddress {
		return nil, fmt.Errorf("%w: invalid from address", ErrInvalidSMTPOptions)
	}
	if strings.ContainsAny(options.FromName, "\r\n") {
		return nil, fmt.Errorf("%w: invalid from name", ErrInvalidSMTPOptions)
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be positive", ErrInvalidSMTPOptions)
	}

	return &SMTPSender{
		host:        host,
		port:        options.Port,
		fromAddress: fromAddress,
		fromHeader: (&mail.Address{
			Name:    strings.TrimSpace(options.FromName),
			Address: fromAddress,
		}).String(),
		timeout: options.Timeout,
		deliver: deliverSMTPMessage,
	}, nil
}

// Send 把邮箱验证码构造成 UTF-8 纯文本邮件并交给 Mailpit。
func (s *SMTPSender) Send(
	ctx context.Context,
	message verificationapplication.Message,
) error {
	if message.Channel != authdomain.VerificationChannelEmail {
		return ErrSMTPEmailChannelRequired
	}

	toAddress, err := mail.ParseAddress(message.Destination)
	if err != nil || toAddress.Address != message.Destination {
		return fmt.Errorf("parse verification email destination: %w", err)
	}

	data := buildVerificationEmail(s.fromHeader, toAddress.String(), message)
	address := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	if err := s.deliver(
		ctx,
		address,
		s.host,
		s.fromAddress,
		[]string{toAddress.Address},
		data,
		s.timeout,
	); err != nil {
		return fmt.Errorf("deliver verification email to local SMTP server: %w", err)
	}
	return nil
}

// buildVerificationEmail 创建带标准头部的 UTF-8 纯文本验证码邮件。
func buildVerificationEmail(
	fromHeader string,
	toHeader string,
	message verificationapplication.Message,
) []byte {
	subject := mime.QEncoding.Encode("UTF-8", "RAG 文档平台验证码")
	body := fmt.Sprintf(
		"你的验证码是：%s\r\n\r\n用途：%s\r\n有效期至：%s UTC\r\n\r\n如果不是你本人操作，请忽略本邮件。\r\n",
		message.Code,
		message.Purpose,
		message.ExpiresAt.UTC().Format("2006-01-02 15:04:05"),
	)

	return []byte(strings.Join([]string{
		"From: " + fromHeader,
		"To: " + toHeader,
		"Subject: " + subject,
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=UTF-8",
		"Content-Transfer-Encoding: 8bit",
		"",
		body,
	}, "\r\n"))
}

// deliverSMTPMessage 使用带超时的 TCP 连接完成无认证 SMTP 交付。
func deliverSMTPMessage(
	ctx context.Context,
	address string,
	host string,
	envelopeFrom string,
	recipients []string,
	message []byte,
	timeout time.Duration,
) error {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("dial SMTP server: %w", err)
	}

	deadline := time.Now().Add(timeout)
	if contextDeadline, hasDeadline := ctx.Deadline(); hasDeadline &&
		contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		_ = connection.Close()
		return fmt.Errorf("set SMTP connection deadline: %w", err)
	}

	client, err := smtp.NewClient(connection, host)
	if err != nil {
		_ = connection.Close()
		return fmt.Errorf("create SMTP client: %w", err)
	}
	defer client.Close()

	if err := client.Mail(envelopeFrom); err != nil {
		return fmt.Errorf("set SMTP envelope sender: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("set SMTP envelope recipient: %w", err)
		}
	}

	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("open SMTP message body: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		_ = writer.Close()
		return fmt.Errorf("write SMTP message body: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("close SMTP message body: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("finish SMTP session: %w", err)
	}
	return nil
}
