package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

const loginTimingPassword = "TimingOnly123"

var (
	// ErrInvalidCredentials 统一表示标识符不存在、密码错误或账户不可登录。
	// Handler 不得把这三种情况拆开，否则会帮助攻击者枚举账户。
	ErrInvalidCredentials = errors.New("identifier or password is incorrect")

	// ErrLoginAccountNotFound 是 Repository 告诉 Application 未找到账户的内部类别。
	// Handler 不直接识别它，Application 会统一转换成 ErrInvalidCredentials。
	ErrLoginAccountNotFound = errors.New("login account not found")

	// ErrLoginDependencies 表示登录服务缺少运行所需端口。
	ErrLoginDependencies = errors.New("login service dependencies are incomplete")
)

// LoginInput 是登录用例输入。
type LoginInput struct {
	Identifier string
	Password   string
}

// LoginOutput 是登录成功后交给 Handler 的安全结果。
type LoginOutput struct {
	User             userdomain.User
	SessionToken     string
	SessionExpiresAt time.Time
}

// LoginService 编排账户查找、密码核对和 Session 创建。
type LoginService struct {
	repository        LoginRepository
	passwordHasher    PasswordHasher
	tokenGenerator    SessionTokenGenerator
	dummyPasswordHash string
	now               func() time.Time
	sessionTTL        time.Duration
}

// NewLoginService 创建登录服务，并生成仅用于均衡“不存在账户”耗时的哑密码哈希。
func NewLoginService(
	repository LoginRepository,
	passwordHasher PasswordHasher,
	tokenGenerator SessionTokenGenerator,
	now func() time.Time,
	sessionTTL time.Duration,
) (*LoginService, error) {
	if repository == nil || passwordHasher == nil || tokenGenerator == nil {
		return nil, ErrLoginDependencies
	}
	if now == nil {
		now = time.Now
	}
	if sessionTTL <= 0 {
		sessionTTL = DefaultSessionTTL
	}

	dummyPasswordHash, err := passwordHasher.Hash(loginTimingPassword)
	if err != nil {
		return nil, fmt.Errorf("create login timing hash: %w", err)
	}

	return &LoginService{
		repository:        repository,
		passwordHasher:    passwordHasher,
		tokenGenerator:    tokenGenerator,
		dummyPasswordHash: dummyPasswordHash,
		now:               now,
		sessionTTL:        sessionTTL,
	}, nil
}

// Login 核对凭据并创建一个新的持久 Session。
func (s *LoginService) Login(
	ctx context.Context,
	input LoginInput,
) (LoginOutput, error) {
	normalizedIdentifier, err := authdomain.NormalizeLoginIdentifier(input.Identifier)
	if err != nil || input.Password == "" || len(input.Password) > userdomain.MaxPasswordBytes {
		return LoginOutput{}, ErrInvalidCredentials
	}

	account, err := s.repository.FindLoginAccount(ctx, normalizedIdentifier)
	if errors.Is(err, ErrLoginAccountNotFound) {
		// 即使账户不存在也执行一次真实 Argon2id，减小响应耗时暴露账户存在性的差异。
		if _, verifyErr := s.passwordHasher.Verify(input.Password, s.dummyPasswordHash); verifyErr != nil {
			return LoginOutput{}, fmt.Errorf("verify login timing password: %w", verifyErr)
		}
		return LoginOutput{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginOutput{}, fmt.Errorf("find login account: %w", err)
	}

	passwordMatches, err := s.passwordHasher.Verify(input.Password, account.PasswordHash)
	if err != nil {
		return LoginOutput{}, fmt.Errorf("verify login password: %w", err)
	}
	if !passwordMatches || !account.User.Status.AllowsAuthentication() {
		return LoginOutput{}, ErrInvalidCredentials
	}

	tokenPair, err := s.tokenGenerator.Generate()
	if err != nil {
		return LoginOutput{}, fmt.Errorf("generate login session token: %w", err)
	}
	now := s.now().UTC()
	createdSession, err := s.repository.CreateSession(
		ctx,
		SessionRecord{
			UserID:    account.User.ID,
			TokenHash: tokenPair.Hash,
			ExpiresAt: now.Add(s.sessionTTL),
			CreatedAt: now,
		},
	)
	if errors.Is(err, ErrInvalidCredentials) {
		return LoginOutput{}, ErrInvalidCredentials
	}
	if err != nil {
		return LoginOutput{}, fmt.Errorf("create login session: %w", err)
	}

	return LoginOutput{
		User:             account.User,
		SessionToken:     tokenPair.Raw,
		SessionExpiresAt: createdSession.ExpiresAt,
	}, nil
}
