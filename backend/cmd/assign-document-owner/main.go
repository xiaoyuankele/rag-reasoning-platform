// Command assign-document-owner 把个人模式上线前的历史无主文档认领给一个已验证用户。
// 默认只执行 dry-run；只有显式提供 -confirm 和匹配的 -expected-unowned 才会写数据库。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	documentapplication "rag-reasoning-platform/backend/internal/application/document"
	"rag-reasoning-platform/backend/internal/config"
	"rag-reasoning-platform/backend/internal/infrastructure/database"
	"rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "assign document owner:", err)
		os.Exit(1)
	}
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	flags := flag.NewFlagSet("assign-document-owner", flag.ContinueOnError)
	flags.SetOutput(stderr)
	ownerUserID := flags.Int64(
		"owner-user-id",
		0,
		"接收全部历史无主文档的用户 ID（必填）",
	)
	confirm := flags.Bool(
		"confirm",
		false,
		"确认执行数据库更新；省略时只做 dry-run",
	)
	expectedUnowned := flags.Int64(
		"expected-unowned",
		-1,
		"确认执行前 dry-run 显示的无主文档数量",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	if *ownerUserID <= 0 {
		return documentapplication.ErrInvalidOwnerClaimUserID
	}
	if *confirm && *expectedUnowned < 0 {
		return fmt.Errorf(
			"-expected-unowned is required when -confirm is provided",
		)
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

	repository := postgres.NewDocumentOwnerClaimRepository(pool)
	service := documentapplication.NewOwnerClaimService(repository)

	if !*confirm {
		preview, err := service.Preview(ctx, *ownerUserID)
		if err != nil {
			return err
		}
		printPreview(stdout, preview)
		return nil
	}

	result, err := service.Claim(ctx, *ownerUserID, *expectedUnowned)
	if err != nil {
		return err
	}
	printResult(stdout, result)
	return nil
}

func printPreview(
	output io.Writer,
	preview documentapplication.OwnerClaimPreview,
) {
	fmt.Fprintln(output, "DRY RUN: no database rows were changed")
	fmt.Fprintf(output, "target_user_id: %d\n", preview.Target.UserID)
	fmt.Fprintf(output, "target_display_name: %s\n", preview.Target.DisplayName)
	fmt.Fprintf(output, "target_status: %s\n", preview.Target.Status)
	fmt.Fprintf(output, "unowned_documents: %d\n", preview.UnownedDocuments)
	fmt.Fprintln(output, "")
	fmt.Fprintln(output, "To apply exactly this plan, run:")
	fmt.Fprintf(
		output,
		"go run ./cmd/assign-document-owner -owner-user-id %d -confirm -expected-unowned %d\n",
		preview.Target.UserID,
		preview.UnownedDocuments,
	)
}

func printResult(
	output io.Writer,
	result documentapplication.OwnerClaimResult,
) {
	fmt.Fprintln(output, "OWNER CLAIM COMMITTED")
	fmt.Fprintf(output, "target_user_id: %d\n", result.Target.UserID)
	fmt.Fprintf(output, "target_display_name: %s\n", result.Target.DisplayName)
	fmt.Fprintf(output, "claimed_documents: %d\n", result.ClaimedDocuments)
	fmt.Fprintf(output, "remaining_unowned_documents: %d\n", result.RemainingUnowned)
}
