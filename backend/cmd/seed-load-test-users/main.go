// Command seed-load-test-users 安全、幂等地预置压测账号并生成非敏感 CSV manifest。
// 默认只展示 dry-run；只有显式确认数量、提供密码环境变量和 manifest 路径才写数据库。
package main

import (
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"syscall"
	"time"

	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	passwordinfrastructure "rag-reasoning-platform/backend/internal/infrastructure/password"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
	loadtestuser "rag-reasoning-platform/backend/internal/maintenance/loadtestuser"
)

var environmentNamePattern = regexp.MustCompile("^[A-Z_][A-Z0-9_]*$")

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "seed load-test users:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	flags := flag.NewFlagSet("seed-load-test-users", flag.ContinueOnError)
	flags.SetOutput(stderr)
	count := flags.Int("count", loadtestuser.DefaultAccountCount, "测试账号数量")
	emailPrefix := flags.String("email-prefix", "loadtest", "邮箱本地部分前缀")
	emailDomain := flags.String(
		"email-domain",
		"example.invalid",
		"必须以 .invalid 结尾的测试域名",
	)
	displayPrefix := flags.String(
		"display-prefix",
		"Load Test User",
		"显示名前缀",
	)
	passwordEnvironment := flags.String(
		"password-env",
		"LOAD_TEST_USER_PASSWORD",
		"读取测试密码的环境变量名称",
	)
	manifestPath := flags.String(
		"manifest",
		"",
		"确认执行后写入的新 CSV manifest 路径",
	)
	confirm := flags.Bool("confirm", false, "确认写入测试数据库")
	expectedCount := flags.Int(
		"expected-count",
		-1,
		"确认 dry-run 中显示的账号数量",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	plan, err := loadtestuser.BuildPlan(loadtestuser.PlanOptions{
		Count:         *count,
		EmailPrefix:   *emailPrefix,
		EmailDomain:   *emailDomain,
		DisplayPrefix: *displayPrefix,
	})
	if err != nil {
		return err
	}
	if !*confirm {
		printPreview(stdout, plan)
		return nil
	}
	if *expectedCount != len(plan) {
		return fmt.Errorf(
			"-expected-count must equal the dry-run count %d",
			len(plan),
		)
	}
	if *manifestPath == "" {
		return errors.New("-manifest is required when -confirm is provided")
	}
	if !environmentNamePattern.MatchString(*passwordEnvironment) {
		return errors.New(
			"-password-env must be an uppercase environment variable name",
		)
	}
	password := os.Getenv(*passwordEnvironment)
	if password == "" {
		return fmt.Errorf("%s must be provided", *passwordEnvironment)
	}
	if err := ensureNewManifestPath(*manifestPath); err != nil {
		return err
	}

	databaseConfig, err := config.LoadDatabase()
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}
	pool, err := database.Open(ctx, databaseConfig.ConnectionString())
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer pool.Close()

	hasher, err := passwordinfrastructure.NewArgon2idHasher(
		passwordinfrastructure.DefaultParameters(),
	)
	if err != nil {
		return fmt.Errorf("create password hasher: %w", err)
	}
	service, err := loadtestuser.NewService(
		postgres.NewLoadTestUserRepository(pool),
		hasher,
		time.Now,
	)
	if err != nil {
		return err
	}

	records, err := service.Seed(ctx, plan, password)
	if err != nil {
		return err
	}
	if err := writeManifest(*manifestPath, records); err != nil {
		return err
	}
	printResult(stdout, records, *manifestPath)
	return nil
}

func printPreview(
	output io.Writer,
	plan []loadtestuser.AccountSpec,
) {
	fmt.Fprintln(
		output,
		"DRY RUN: no database rows or manifest files were changed",
	)
	fmt.Fprintf(output, "account_count: %d\n", len(plan))
	fmt.Fprintf(output, "first_email: %s\n", plan[0].Email)
	fmt.Fprintf(output, "last_email: %s\n", plan[len(plan)-1].Email)
	fmt.Fprintln(output, roleSummary(plan))
	fmt.Fprintln(
		output,
		"To commit, provide the password through LOAD_TEST_USER_PASSWORD and use:",
	)
	fmt.Fprintf(
		output,
		"go run ./cmd/seed-load-test-users -count %d -confirm "+
			"-expected-count %d -manifest <new-manifest.csv>\n",
		len(plan),
		len(plan),
	)
}

func printResult(
	output io.Writer,
	records []loadtestuser.Record,
	manifestPath string,
) {
	created := 0
	existing := 0
	for _, record := range records {
		switch record.Outcome {
		case loadtestuser.OutcomeCreated:
			created++
		case loadtestuser.OutcomeExisting:
			existing++
		}
	}
	fmt.Fprintln(output, "LOAD-TEST ACCOUNTS COMMITTED")
	fmt.Fprintf(output, "account_count: %d\n", len(records))
	fmt.Fprintf(output, "created: %d\n", created)
	fmt.Fprintf(output, "existing_verified: %d\n", existing)
	fmt.Fprintf(output, "manifest: %s\n", manifestPath)
}

func ensureNewManifestPath(path string) error {
	if info, err := os.Stat(path); err == nil {
		return fmt.Errorf(
			"manifest already exists and will not be overwritten: %s (%d bytes)",
			path,
			info.Size(),
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect manifest path: %w", err)
	}
	return nil
}

func writeManifest(
	path string,
	records []loadtestuser.Record,
) (returnErr error) {
	if err := ensureNewManifestPath(path); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o750); err != nil {
		return fmt.Errorf("create manifest directory: %w", err)
	}

	temporary, err := os.CreateTemp(directory, ".load-test-users-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary manifest: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if returnErr != nil {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict temporary manifest permissions: %w", err)
	}

	writer := csv.NewWriter(temporary)
	if err := writer.Write([]string{
		"index",
		"email",
		"display_name",
		"user_id",
		"outcome",
		"created_at_utc",
		"role",
	}); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}
	for _, record := range records {
		if err := writer.Write([]string{
			strconv.Itoa(record.Index),
			record.Email,
			record.DisplayName,
			strconv.FormatInt(record.UserID, 10),
			string(record.Outcome),
			record.CreatedAt.UTC().Format(time.RFC3339Nano),
			string(record.Role),
		}); err != nil {
			return fmt.Errorf("write manifest record %d: %w", record.Index, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush manifest CSV: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync manifest: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close manifest: %w", err)
	}
	// 临时文件与目标文件位于同一目录。硬链接发布使用“目标必须不存在”语义，
	// 避免另一个进程在前置检查之后创建同名文件时被 os.Rename 覆盖。
	if err := os.Link(temporaryPath, path); err != nil {
		return fmt.Errorf("publish manifest: %w", err)
	}
	if err := os.Remove(temporaryPath); err != nil {
		return fmt.Errorf("remove published manifest temporary link: %w", err)
	}
	return nil
}

func roleSummary(plan []loadtestuser.AccountSpec) string {
	counts := make(map[loadtestuser.Role]int)
	for _, spec := range plan {
		counts[spec.Role]++
	}
	return fmt.Sprintf(
		"roles: browser=%d search=%d observer=%d uploader=%d session=%d reserve=%d",
		counts[loadtestuser.RoleBrowser],
		counts[loadtestuser.RoleSearch],
		counts[loadtestuser.RoleObserver],
		counts[loadtestuser.RoleUploader],
		counts[loadtestuser.RoleSession],
		counts[loadtestuser.RoleReserve],
	)
}
