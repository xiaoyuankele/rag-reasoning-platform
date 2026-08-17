package verification

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	verificationapplication "rag-reasoning-platform/backend/internal/application/verification"
	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

func TestSMTPSenderBuildsVerificationEmail(t *testing.T) {
	sender := newTestSMTPSender(t)
	var capturedAddress, capturedHost, capturedFrom string
	var capturedRecipients []string
	var capturedMessage []byte
	sender.deliver = func(
		_ context.Context,
		address string,
		host string,
		envelopeFrom string,
		recipients []string,
		message []byte,
		_ time.Duration,
	) error {
		capturedAddress = address
		capturedHost = host
		capturedFrom = envelopeFrom
		capturedRecipients = append([]string(nil), recipients...)
		capturedMessage = append([]byte(nil), message...)
		return nil
	}

	err := sender.Send(
		context.Background(),
		verificationapplication.Message{
			ChallengeID: 17,
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "learner@example.com",
			Purpose:     authdomain.VerificationPurposeRegister,
			Code:        "483921",
			ExpiresAt:   time.Date(2026, 8, 17, 10, 30, 0, 0, time.FixedZone("CST", 8*60*60)),
		},
	)
	if err != nil {
		t.Fatalf("Send() error = %v, want nil", err)
	}

	if capturedAddress != "127.0.0.1:1025" ||
		capturedHost != "127.0.0.1" ||
		capturedFrom != "no-reply@rag.local" ||
		len(capturedRecipients) != 1 ||
		capturedRecipients[0] != "learner@example.com" {
		t.Fatalf(
			"SMTP envelope = (%q, %q, %q, %v), want local Mailpit envelope",
			capturedAddress,
			capturedHost,
			capturedFrom,
			capturedRecipients,
		)
	}

	email := string(capturedMessage)
	for _, wanted := range []string{
		"From: \"RAG Reasoning Platform\" <no-reply@rag.local>",
		"To: <learner@example.com>",
		"Subject: =?UTF-8?",
		"Content-Type: text/plain; charset=UTF-8",
		"483921",
		"register",
		"2026-08-17 02:30:00 UTC",
	} {
		if !strings.Contains(email, wanted) {
			t.Fatalf("verification email = %q, want substring %q", email, wanted)
		}
	}
}

func TestSMTPSenderRejectsNonEmailChannel(t *testing.T) {
	sender := newTestSMTPSender(t)
	sender.deliver = func(
		context.Context,
		string,
		string,
		string,
		[]string,
		[]byte,
		time.Duration,
	) error {
		t.Fatal("SMTP delivery was called for a phone challenge")
		return nil
	}

	err := sender.Send(
		context.Background(),
		verificationapplication.Message{
			Channel:     authdomain.VerificationChannelSMS,
			Destination: "+8613800138000",
			Purpose:     authdomain.VerificationPurposeRegister,
			Code:        "123456",
		},
	)
	if !errors.Is(err, ErrSMTPEmailChannelRequired) {
		t.Fatalf("Send() error = %v, want %v", err, ErrSMTPEmailChannelRequired)
	}
}

func TestSMTPSenderWrapsDeliveryError(t *testing.T) {
	sender := newTestSMTPSender(t)
	wantedError := errors.New("mailpit unavailable")
	sender.deliver = func(
		context.Context,
		string,
		string,
		string,
		[]string,
		[]byte,
		time.Duration,
	) error {
		return wantedError
	}

	err := sender.Send(
		context.Background(),
		verificationapplication.Message{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "learner@example.com",
			Purpose:     authdomain.VerificationPurposeRegister,
			Code:        "123456",
		},
	)
	if !errors.Is(err, wantedError) {
		t.Fatalf("Send() error = %v, want wrapped %v", err, wantedError)
	}
}

func TestNewSMTPSenderRejectsInvalidOptions(t *testing.T) {
	tests := []struct {
		name    string
		options SMTPOptions
	}{
		{name: "missing host", options: SMTPOptions{Port: 1025, FromAddress: "no-reply@rag.local", Timeout: time.Second}},
		{name: "invalid port", options: SMTPOptions{Host: "localhost", Port: 0, FromAddress: "no-reply@rag.local", Timeout: time.Second}},
		{name: "invalid from", options: SMTPOptions{Host: "localhost", Port: 1025, FromAddress: "RAG <no-reply@rag.local>", Timeout: time.Second}},
		{name: "invalid name", options: SMTPOptions{Host: "localhost", Port: 1025, FromAddress: "no-reply@rag.local", FromName: "RAG\r\nBcc: attacker@example.com", Timeout: time.Second}},
		{name: "invalid timeout", options: SMTPOptions{Host: "localhost", Port: 1025, FromAddress: "no-reply@rag.local"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewSMTPSender(test.options)
			if !errors.Is(err, ErrInvalidSMTPOptions) {
				t.Fatalf("NewSMTPSender() error = %v, want %v", err, ErrInvalidSMTPOptions)
			}
		})
	}
}

func newTestSMTPSender(t *testing.T) *SMTPSender {
	t.Helper()
	sender, err := NewSMTPSender(
		SMTPOptions{
			Host:        "127.0.0.1",
			Port:        1025,
			FromAddress: "no-reply@rag.local",
			FromName:    "RAG Reasoning Platform",
			Timeout:     5 * time.Second,
		},
	)
	if err != nil {
		t.Fatalf("NewSMTPSender() error = %v, want nil", err)
	}
	return sender
}
