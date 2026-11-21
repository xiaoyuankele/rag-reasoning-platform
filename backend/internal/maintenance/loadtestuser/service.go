// Package loadtestuser 提供性能测试账号的安全规划和幂等预置用例。
// 它只用于受控测试环境，不属于普通用户注册链路。
package loadtestuser

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	authdomain "rag-reasoning-platform/backend/internal/domain/auth"
	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

const (
	// DefaultAccountCount 是第一轮 100 VU 加 10 个备用账号的数量。
	DefaultAccountCount = 110

	// MaxAccountCount 防止误操作一次创建无边界数量的数据库账号。
	MaxAccountCount = 10_000
)

var (
	// ErrInvalidAccountCount 表示计划数量不在安全范围内。
	ErrInvalidAccountCount = errors.New("load-test account count must be between 1 and 10000")

	// ErrInvalidEmailPrefix 表示邮箱本地部分前缀不安全。
	ErrInvalidEmailPrefix = errors.New("load-test email prefix is invalid")

	// ErrUnsafeEmailDomain 表示邮箱没有使用保留的 .invalid 测试域名。
	ErrUnsafeEmailDomain = errors.New("load-test email domain must end with .invalid")

	// ErrEmptyPlan 表示调用 Seed 时没有提供账号计划。
	ErrEmptyPlan = errors.New("load-test account plan must not be empty")

	// ErrSeedDependencies 表示 Seeder 缺少仓储或密码哈希器。
	ErrSeedDependencies = errors.New("load-test account seeder dependencies are incomplete")

	emailPrefixPattern = regexp.MustCompile("^[a-z0-9][a-z0-9-]{0,39}$")
)

// Role 表示账号在 100 用户测试中的固定行为分组。
type Role string

const (
	RoleBrowser  Role = "browser"
	RoleSearch   Role = "search"
	RoleObserver Role = "observer"
	RoleUploader Role = "uploader"
	RoleSession  Role = "session"
	RoleReserve  Role = "reserve"
)

// PlanOptions 控制确定性账号清单的生成方式。
type PlanOptions struct {
	Count         int
	EmailPrefix   string
	EmailDomain   string
	DisplayPrefix string
}

// AccountSpec 是不含密码和数据库状态的预期账号。
type AccountSpec struct {
	Index       int
	Email       string
	DisplayName string
	Role        Role
}

// ExistingAccount 是仓储返回给维护用例的最小凭据摘要。
// PasswordHash 只能用于本地核对，不能进入命令输出或 manifest。
type ExistingAccount struct {
	ID           int64
	Email        string
	DisplayName  string
	PasswordHash string
	Status       userdomain.Status
	CreatedAt    time.Time
}

// NewAccount 是已经完成密码哈希、可以写入数据库的测试账号。
type NewAccount struct {
	Email        string
	DisplayName  string
	PasswordHash string
}

// StoredAccount 是仓储创建账号后的安全结果。
type StoredAccount struct {
	ID          int64
	Email       string
	DisplayName string
	CreatedAt   time.Time
}

// Outcome 描述账号是本轮创建还是之前已经存在。
type Outcome string

const (
	OutcomeCreated  Outcome = "created"
	OutcomeExisting Outcome = "existing"
)

// Record 是可以写入非敏感 manifest 的账号记录。
type Record struct {
	Index       int
	Email       string
	DisplayName string
	Role        Role
	UserID      int64
	Outcome     Outcome
	CreatedAt   time.Time
}

// PasswordMismatchError 表示既有账号不能使用本轮提供的测试密码。
// Seeder 不会覆盖密码，调用方应核对测试环境或改用新的账号前缀。
type PasswordMismatchError struct {
	Email string
}

func (e *PasswordMismatchError) Error() string {
	return fmt.Sprintf("existing load-test account %s uses a different password", e.Email)
}

// ExistingAccountMismatchError 表示同邮箱账号不是当前确定性计划预期的账号。
type ExistingAccountMismatchError struct {
	Email               string
	ExpectedDisplayName string
	ActualDisplayName   string
	Status              userdomain.Status
}

func (e *ExistingAccountMismatchError) Error() string {
	return fmt.Sprintf("existing load-test account %s does not match the plan", e.Email)
}

// PasswordHasher 是维护用例需要的最小 Argon2id 端口。
type PasswordHasher interface {
	Hash(password string) (string, error)
	Verify(password, encodedHash string) (bool, error)
}

// Repository 是测试账号预置所需的最小持久化端口。
type Repository interface {
	FindExistingAccounts(
		ctx context.Context,
		emails []string,
	) ([]ExistingAccount, error)
	CreateAccounts(
		ctx context.Context,
		accounts []NewAccount,
		createdAt time.Time,
	) ([]StoredAccount, error)
}

// Service 负责验证既有账号、生成密码哈希并创建缺失账号。
type Service struct {
	repository Repository
	hasher     PasswordHasher
	now        func() time.Time
}

// NewService 创建测试账号 Seeder。
func NewService(
	repository Repository,
	hasher PasswordHasher,
	now func() time.Time,
) (*Service, error) {
	if repository == nil || hasher == nil {
		return nil, ErrSeedDependencies
	}
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, hasher: hasher, now: now}, nil
}

// BuildPlan 生成稳定、可复现且只使用 .invalid 域名的账号计划。
func BuildPlan(options PlanOptions) ([]AccountSpec, error) {
	if options.Count < 1 || options.Count > MaxAccountCount {
		return nil, ErrInvalidAccountCount
	}

	emailPrefix := strings.ToLower(strings.TrimSpace(options.EmailPrefix))
	if !emailPrefixPattern.MatchString(emailPrefix) {
		return nil, ErrInvalidEmailPrefix
	}

	emailDomain := strings.ToLower(strings.TrimSpace(options.EmailDomain))
	if !strings.HasSuffix(emailDomain, ".invalid") {
		return nil, ErrUnsafeEmailDomain
	}

	displayPrefix, err := userdomain.NormalizeDisplayName(options.DisplayPrefix)
	if err != nil {
		return nil, err
	}

	width := len(strconv.Itoa(options.Count))
	if width < 3 {
		width = 3
	}

	plan := make([]AccountSpec, 0, options.Count)
	for index := 1; index <= options.Count; index++ {
		email := fmt.Sprintf(
			"%s-%0*d@%s",
			emailPrefix,
			width,
			index,
			emailDomain,
		)
		normalizedEmail, normalizeErr := authdomain.NormalizeVerificationDestination(
			authdomain.VerificationChannelEmail,
			email,
		)
		if normalizeErr != nil {
			return nil, fmt.Errorf("build account %d email: %w", index, normalizeErr)
		}

		displayName, normalizeErr := userdomain.NormalizeDisplayName(
			fmt.Sprintf("%s %0*d", displayPrefix, width, index),
		)
		if normalizeErr != nil {
			return nil, fmt.Errorf("build account %d display name: %w", index, normalizeErr)
		}

		plan = append(plan, AccountSpec{
			Index:       index,
			Email:       normalizedEmail,
			DisplayName: displayName,
			Role:        roleForIndex(index),
		})
	}
	return plan, nil
}

// Seed 幂等创建缺失账号，并核对所有既有账号仍可使用指定密码。
// 它不会修改既有用户、密码或状态。
func (s *Service) Seed(
	ctx context.Context,
	plan []AccountSpec,
	password string,
) ([]Record, error) {
	if len(plan) == 0 {
		return nil, ErrEmptyPlan
	}
	if err := userdomain.ValidatePassword(password); err != nil {
		return nil, err
	}

	emails := make([]string, 0, len(plan))
	seen := make(map[string]struct{}, len(plan))
	for _, spec := range plan {
		if _, duplicate := seen[spec.Email]; duplicate {
			return nil, fmt.Errorf("duplicate load-test account email %s", spec.Email)
		}
		seen[spec.Email] = struct{}{}
		emails = append(emails, spec.Email)
	}

	existingAccounts, err := s.repository.FindExistingAccounts(ctx, emails)
	if err != nil {
		return nil, fmt.Errorf("find existing load-test accounts: %w", err)
	}
	existingByEmail := make(map[string]ExistingAccount, len(existingAccounts))
	for _, account := range existingAccounts {
		if _, expected := seen[account.Email]; !expected {
			return nil, fmt.Errorf("repository returned unexpected account %s", account.Email)
		}
		existingByEmail[account.Email] = account
	}

	newAccounts := make([]NewAccount, 0, len(plan)-len(existingByEmail))
	for _, spec := range plan {
		existing, found := existingByEmail[spec.Email]
		if !found {
			passwordHash, hashErr := s.hasher.Hash(password)
			if hashErr != nil {
				return nil, fmt.Errorf("hash password for %s: %w", spec.Email, hashErr)
			}
			newAccounts = append(newAccounts, NewAccount{
				Email:        spec.Email,
				DisplayName:  spec.DisplayName,
				PasswordHash: passwordHash,
			})
			continue
		}

		if existing.DisplayName != spec.DisplayName ||
			existing.Status != userdomain.StatusActive {
			return nil, &ExistingAccountMismatchError{
				Email:               spec.Email,
				ExpectedDisplayName: spec.DisplayName,
				ActualDisplayName:   existing.DisplayName,
				Status:              existing.Status,
			}
		}
		matches, verifyErr := s.hasher.Verify(password, existing.PasswordHash)
		if verifyErr != nil {
			return nil, fmt.Errorf("verify password for %s: %w", spec.Email, verifyErr)
		}
		if !matches {
			return nil, &PasswordMismatchError{Email: spec.Email}
		}
	}

	createdAt := s.now().UTC()
	createdAccounts, err := s.repository.CreateAccounts(ctx, newAccounts, createdAt)
	if err != nil {
		return nil, fmt.Errorf("create missing load-test accounts: %w", err)
	}
	if len(createdAccounts) != len(newAccounts) {
		return nil, fmt.Errorf(
			"created account count mismatch: expected %d, got %d",
			len(newAccounts),
			len(createdAccounts),
		)
	}

	createdByEmail := make(map[string]StoredAccount, len(createdAccounts))
	for _, account := range createdAccounts {
		createdByEmail[account.Email] = account
	}

	records := make([]Record, 0, len(plan))
	for _, spec := range plan {
		if existing, found := existingByEmail[spec.Email]; found {
			records = append(records, Record{
				Index:       spec.Index,
				Email:       spec.Email,
				DisplayName: spec.DisplayName,
				Role:        spec.Role,
				UserID:      existing.ID,
				Outcome:     OutcomeExisting,
				CreatedAt:   existing.CreatedAt.UTC(),
			})
			continue
		}
		created, found := createdByEmail[spec.Email]
		if !found {
			return nil, fmt.Errorf("created account %s was not returned", spec.Email)
		}
		records = append(records, Record{
			Index:       spec.Index,
			Email:       spec.Email,
			DisplayName: spec.DisplayName,
			Role:        spec.Role,
			UserID:      created.ID,
			Outcome:     OutcomeCreated,
			CreatedAt:   created.CreatedAt.UTC(),
		})
	}
	return records, nil
}

func roleForIndex(index int) Role {
	switch {
	case index <= 40:
		return RoleBrowser
	case index <= 65:
		return RoleSearch
	case index <= 85:
		return RoleObserver
	case index <= 95:
		return RoleUploader
	case index <= 100:
		return RoleSession
	default:
		return RoleReserve
	}
}
