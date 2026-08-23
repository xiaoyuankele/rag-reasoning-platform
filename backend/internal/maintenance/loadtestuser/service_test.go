package loadtestuser

import (
	"context"
	"errors"
	"testing"
	"time"

	userdomain "rag-reasoning-platform/backend/internal/domain/user"
)

func TestBuildPlanCreatesDeterministicRoles(t *testing.T) {
	plan, err := BuildPlan(PlanOptions{
		Count:         110,
		EmailPrefix:   "loadtest",
		EmailDomain:   "example.invalid",
		DisplayPrefix: "Load Test User",
	})
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if len(plan) != 110 {
		t.Fatalf("plan length = %d, want 110", len(plan))
	}
	checks := []struct {
		index int
		email string
		role  Role
	}{
		{1, "loadtest-001@example.invalid", RoleBrowser},
		{40, "loadtest-040@example.invalid", RoleBrowser},
		{41, "loadtest-041@example.invalid", RoleSearch},
		{66, "loadtest-066@example.invalid", RoleObserver},
		{86, "loadtest-086@example.invalid", RoleUploader},
		{96, "loadtest-096@example.invalid", RoleSession},
		{101, "loadtest-101@example.invalid", RoleReserve},
		{110, "loadtest-110@example.invalid", RoleReserve},
	}
	for _, check := range checks {
		actual := plan[check.index-1]
		if actual.Email != check.email || actual.Role != check.role {
			t.Errorf(
				"plan[%d] = %+v, want email=%s role=%s",
				check.index-1,
				actual,
				check.email,
				check.role,
			)
		}
	}
}

func TestBuildPlanRejectsUnsafeInputs(t *testing.T) {
	valid := PlanOptions{
		Count:         110,
		EmailPrefix:   "loadtest",
		EmailDomain:   "example.invalid",
		DisplayPrefix: "Load Test User",
	}
	tests := []struct {
		name    string
		change  func(*PlanOptions)
		wantErr error
	}{
		{"zero count", func(options *PlanOptions) { options.Count = 0 }, ErrInvalidAccountCount},
		{"too many", func(options *PlanOptions) { options.Count = MaxAccountCount + 1 }, ErrInvalidAccountCount},
		{"unsafe prefix", func(options *PlanOptions) { options.EmailPrefix = "load@test" }, ErrInvalidEmailPrefix},
		{"real domain", func(options *PlanOptions) { options.EmailDomain = "example.com" }, ErrUnsafeEmailDomain},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			options := valid
			testCase.change(&options)
			_, err := BuildPlan(options)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("BuildPlan() error = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func TestServiceSeedsMissingAndVerifiesExistingAccounts(t *testing.T) {
	now := time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC)
	plan, err := BuildPlan(PlanOptions{
		Count:         2,
		EmailPrefix:   "loadtest",
		EmailDomain:   "example.invalid",
		DisplayPrefix: "Load Test User",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{
		existing: []ExistingAccount{{
			ID:           7,
			Email:        plan[0].Email,
			DisplayName:  plan[0].DisplayName,
			PasswordHash: "hash:Example123",
			Status:       userdomain.StatusActive,
			CreatedAt:    now.Add(-time.Hour),
		}},
		nextID: 8,
	}
	service, err := NewService(repository, fakeHasher{}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	records, err := service.Seed(context.Background(), plan, "Example123")
	if err != nil {
		t.Fatalf("Seed() error = %v", err)
	}
	if len(repository.created) != 1 || repository.created[0].Email != plan[1].Email {
		t.Fatalf("created accounts = %+v, want only second account", repository.created)
	}
	if len(records) != 2 ||
		records[0].Outcome != OutcomeExisting ||
		records[1].Outcome != OutcomeCreated {
		t.Fatalf("records = %+v", records)
	}
	if records[0].UserID != 7 || records[1].UserID != 8 {
		t.Fatalf("record user IDs = %d,%d, want 7,8", records[0].UserID, records[1].UserID)
	}
}

func TestServiceDoesNotOverwriteMismatchedExistingAccount(t *testing.T) {
	plan, err := BuildPlan(PlanOptions{
		Count:         1,
		EmailPrefix:   "loadtest",
		EmailDomain:   "example.invalid",
		DisplayPrefix: "Load Test User",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{existing: []ExistingAccount{{
		ID:           7,
		Email:        plan[0].Email,
		DisplayName:  plan[0].DisplayName,
		PasswordHash: "hash:Different123",
		Status:       userdomain.StatusActive,
	}}}
	service, err := NewService(repository, fakeHasher{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}

	_, err = service.Seed(context.Background(), plan, "Example123")
	var mismatch *PasswordMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("Seed() error = %v, want PasswordMismatchError", err)
	}
	if len(repository.created) != 0 {
		t.Fatalf("created accounts = %+v, want none", repository.created)
	}
}

func TestServiceValidatesPasswordBeforeRepository(t *testing.T) {
	plan, err := BuildPlan(PlanOptions{
		Count:         1,
		EmailPrefix:   "loadtest",
		EmailDomain:   "example.invalid",
		DisplayPrefix: "Load Test User",
	})
	if err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{}
	service, err := NewService(repository, fakeHasher{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Seed(context.Background(), plan, "password")
	if !errors.Is(err, userdomain.ErrPasswordMissingUppercase) {
		t.Fatalf("Seed() error = %v, want password policy error", err)
	}
	if repository.findCalled {
		t.Fatal("repository was called for invalid password")
	}
}

type fakeHasher struct{}

func (fakeHasher) Hash(password string) (string, error) {
	return "hash:" + password, nil
}

func (fakeHasher) Verify(password string, encodedHash string) (bool, error) {
	return encodedHash == "hash:"+password, nil
}

type fakeRepository struct {
	existing   []ExistingAccount
	created    []NewAccount
	findCalled bool
	nextID     int64
}

func (r *fakeRepository) FindExistingAccounts(
	_ context.Context,
	_ []string,
) ([]ExistingAccount, error) {
	r.findCalled = true
	return append([]ExistingAccount(nil), r.existing...), nil
}

func (r *fakeRepository) CreateAccounts(
	_ context.Context,
	accounts []NewAccount,
	createdAt time.Time,
) ([]StoredAccount, error) {
	r.created = append([]NewAccount(nil), accounts...)
	results := make([]StoredAccount, 0, len(accounts))
	for _, account := range accounts {
		results = append(results, StoredAccount{
			ID:          r.nextID,
			Email:       account.Email,
			DisplayName: account.DisplayName,
			CreatedAt:   createdAt,
		})
		r.nextID++
	}
	return results, nil
}
