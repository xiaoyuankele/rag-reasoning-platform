package verification

import (
	"context"
	"errors"
	"testing"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
)

func TestServiceRequestCode(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	repository := &fakeRepository{
		findErr: authdomain.ErrVerificationChallengeNotFound,
	}
	generator := &fakeCodeGenerator{code: "004321"}
	hasher := &fakeCodeHasher{hash: "test-code-hash"}
	sender := &fakeSender{}
	service := NewService(
		repository,
		generator,
		hasher,
		sender,
		func() time.Time { return fixedNow },
		10*time.Minute,
		time.Minute,
	)

	output, err := service.RequestCode(
		context.Background(),
		RequestInput{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "  Owner@Example.COM ",
			Purpose:     authdomain.VerificationPurposeRegister,
		},
	)
	if err != nil {
		t.Fatalf("RequestCode() error = %v, want nil", err)
	}

	if output.ChallengeID != 42 {
		t.Fatalf("ChallengeID = %d, want 42", output.ChallengeID)
	}
	if !output.ExpiresAt.Equal(fixedNow.Add(10 * time.Minute)) {
		t.Fatalf("ExpiresAt = %s, want %s", output.ExpiresAt, fixedNow.Add(10*time.Minute))
	}
	if !output.ResendAfter.Equal(fixedNow.Add(time.Minute)) {
		t.Fatalf("ResendAfter = %s, want %s", output.ResendAfter, fixedNow.Add(time.Minute))
	}

	if repository.created.Destination != "owner@example.com" {
		t.Fatalf("created destination = %q, want normalized email", repository.created.Destination)
	}
	if repository.created.CodeHash != "test-code-hash" {
		t.Fatalf("created code hash = %q, want test hash", repository.created.CodeHash)
	}
	if repository.markSentCalls != 1 {
		t.Fatalf("MarkSent() calls = %d, want 1", repository.markSentCalls)
	}
	if len(sender.messages) != 1 || sender.messages[0].Code != "004321" {
		t.Fatalf("sent messages = %+v, want one message with generated code", sender.messages)
	}
	if hasher.destination != "owner@example.com" || hasher.code != "004321" {
		t.Fatalf("hasher received destination=%q code=%q", hasher.destination, hasher.code)
	}
}

func TestServiceRequestCodeEnforcesCooldown(t *testing.T) {
	fixedNow := time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC)
	lastSentAt := fixedNow.Add(-30 * time.Second)
	repository := &fakeRepository{
		found: authdomain.VerificationChallenge{LastSentAt: &lastSentAt},
	}
	generator := &fakeCodeGenerator{code: "123456"}
	service := NewService(
		repository,
		generator,
		&fakeCodeHasher{},
		&fakeSender{},
		func() time.Time { return fixedNow },
		DefaultChallengeTTL,
		DefaultResendCooldown,
	)

	_, err := service.RequestCode(
		context.Background(),
		RequestInput{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "owner@example.com",
			Purpose:     authdomain.VerificationPurposeRegister,
		},
	)
	if !errors.Is(err, ErrVerificationCooldown) {
		t.Fatalf("RequestCode() error = %v, want cooldown", err)
	}

	var cooldownError *CooldownError
	if !errors.As(err, &cooldownError) {
		t.Fatalf("RequestCode() error type = %T, want *CooldownError", err)
	}
	if !cooldownError.RetryAt.Equal(lastSentAt.Add(time.Minute)) {
		t.Fatalf("RetryAt = %s, want %s", cooldownError.RetryAt, lastSentAt.Add(time.Minute))
	}
	if generator.calls != 0 {
		t.Fatalf("Generate() calls = %d, want 0 during cooldown", generator.calls)
	}
}

func TestServiceRequestCodePreservesUnsentChallengeOnSenderFailure(t *testing.T) {
	sendErr := errors.New("provider unavailable")
	repository := &fakeRepository{
		findErr: authdomain.ErrVerificationChallengeNotFound,
	}
	service := NewService(
		repository,
		&fakeCodeGenerator{code: "123456"},
		&fakeCodeHasher{hash: "test-hash"},
		&fakeSender{err: sendErr},
		func() time.Time { return time.Date(2026, time.August, 16, 8, 0, 0, 0, time.UTC) },
		DefaultChallengeTTL,
		DefaultResendCooldown,
	)

	_, err := service.RequestCode(
		context.Background(),
		RequestInput{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "owner@example.com",
			Purpose:     authdomain.VerificationPurposeRegister,
		},
	)
	if !errors.Is(err, sendErr) {
		t.Fatalf("RequestCode() error = %v, want wrapped sender error", err)
	}
	if !errors.Is(err, ErrVerificationDeliveryUnavailable) {
		t.Fatalf("RequestCode() error = %v, want delivery category", err)
	}
	if repository.created.ID == 0 {
		t.Fatal("pending challenge was not created before sending")
	}
	if repository.markSentCalls != 0 {
		t.Fatalf("MarkSent() calls = %d, want 0 after sender failure", repository.markSentCalls)
	}
}

func TestServiceRequestCodeRejectsInvalidInputBeforeDependencies(t *testing.T) {
	repository := &fakeRepository{}
	generator := &fakeCodeGenerator{}
	service := NewService(
		repository,
		generator,
		&fakeCodeHasher{},
		&fakeSender{},
		time.Now,
		DefaultChallengeTTL,
		DefaultResendCooldown,
	)

	_, err := service.RequestCode(
		context.Background(),
		RequestInput{
			Channel:     authdomain.VerificationChannelEmail,
			Destination: "not-an-email",
			Purpose:     authdomain.VerificationPurposeRegister,
		},
	)
	if !errors.Is(err, authdomain.ErrInvalidVerificationDestination) {
		t.Fatalf("RequestCode() error = %v, want invalid destination", err)
	}
	if repository.findCalls != 0 || generator.calls != 0 {
		t.Fatalf("dependencies were called for invalid input")
	}
}

// fakeRepository 是只保存在内存中的 Repository 测试替身。
// 测试可以通过字段预设返回值，并检查 Service 调用了哪些持久化方法。
type fakeRepository struct {
	found         authdomain.VerificationChallenge
	findErr       error
	findCalls     int
	created       authdomain.VerificationChallenge
	createErr     error
	markSentErr   error
	markSentCalls int
}

func (r *fakeRepository) FindLatest(
	_ context.Context,
	_ authdomain.VerificationChannel,
	_ string,
	_ authdomain.VerificationPurpose,
) (authdomain.VerificationChallenge, error) {
	r.findCalls++
	return r.found, r.findErr
}

func (r *fakeRepository) Create(
	_ context.Context,
	challenge authdomain.VerificationChallenge,
	_ time.Duration,
) (authdomain.VerificationChallenge, error) {
	if r.createErr != nil {
		return authdomain.VerificationChallenge{}, r.createErr
	}

	challenge.ID = 42
	r.created = challenge
	return challenge, nil
}

func (r *fakeRepository) MarkSent(
	_ context.Context,
	challengeID int64,
	sentAt time.Time,
) (authdomain.VerificationChallenge, error) {
	r.markSentCalls++
	if r.markSentErr != nil {
		return authdomain.VerificationChallenge{}, r.markSentErr
	}

	marked := r.created
	marked.ID = challengeID
	marked.SendCount++
	marked.LastSentAt = &sentAt
	marked.UpdatedAt = sentAt
	return marked, nil
}

// fakeCodeGenerator 返回测试预先指定的验证码，避免依赖真实随机数。
type fakeCodeGenerator struct {
	code  string
	err   error
	calls int
}

func (g *fakeCodeGenerator) Generate() (string, error) {
	g.calls++
	return g.code, g.err
}

// fakeCodeHasher 记录收到的明文输入，并返回固定摘要。
type fakeCodeHasher struct {
	hash        string
	destination string
	code        string
}

func (h *fakeCodeHasher) Hash(
	_ authdomain.VerificationChannel,
	destination string,
	_ authdomain.VerificationPurpose,
	code string,
) string {
	h.destination = destination
	h.code = code
	return h.hash
}

func (h *fakeCodeHasher) Matches(
	_ string,
	_ authdomain.VerificationChannel,
	_ string,
	_ authdomain.VerificationPurpose,
	_ string,
) bool {
	return false
}

// fakeSender 记录 Application 准备发送的消息，也可以模拟渠道故障。
type fakeSender struct {
	messages []Message
	err      error
}

func (s *fakeSender) Send(_ context.Context, message Message) error {
	if s.err != nil {
		return s.err
	}

	s.messages = append(s.messages, message)
	return nil
}
