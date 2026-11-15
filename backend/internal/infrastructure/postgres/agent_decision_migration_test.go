package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	"rag-reasoning-platform/backend/migrations"
)

func TestAgentDecisionTokenMigrationUpAndDown(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	downSQL, err := os.ReadFile(filepath.Join(
		"..", "..", "..", "migrations",
		"000039_allow_active_agent_step_decision_tokens.down.sql",
	))
	if err != nil {
		t.Fatalf("read migration 39 down: %v", err)
	}
	upSQL, err := migrations.Files.ReadFile("000039_allow_active_agent_step_decision_tokens.up.sql")
	if err != nil {
		t.Fatalf("read migration 39 up: %v", err)
	}
	execScript := func(script []byte) error {
		connection, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer connection.Release()
		_, err = connection.Conn().PgConn().Exec(ctx, string(script)).ReadAll()
		if err != nil {
			_, _ = connection.Exec(ctx, "ROLLBACK")
		}
		return err
	}
	constraintDefinition := func() string {
		var definition string
		if err := pool.QueryRow(
			ctx,
			`SELECT pg_get_constraintdef(oid)
			 FROM pg_constraint
			 WHERE conrelid = 'agent_steps'::REGCLASS
			   AND conname = 'agent_steps_state_payload_check'`,
		).Scan(&definition); err != nil {
			t.Fatalf("read agent Step payload constraint: %v", err)
		}
		return definition
	}

	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-migration-39")
	repository := newAgentTestRepository(pool, "agent-worker-migration-39")
	conversation := createAgentTestConversation(t, ctx, pool, owner, "migration 39 fail closed")
	queueAgentTestRun(t, ctx, repository, owner, conversation.ID, "migration-39-active")
	claimed, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim migration 39 fixture: %v", err)
	}
	step, err := repository.PersistDecision(ctx, claimed.ID, claimed.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"migration"}`),
		PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5,
	})
	if err != nil || step.Status != agentdomain.StepStatusRunning || step.TotalTokens != 5 {
		t.Fatalf("persist active decision tokens = %+v, %v", step, err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_steps SET completion_tokens = NULL WHERE id = $1`, step.ID); err == nil {
		t.Fatal("migration 39 allowed a partial active Token triple")
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_steps SET result_json = '{}'::JSONB WHERE id = $1`, step.ID); err == nil {
		t.Fatal("migration 39 allowed an active Step result")
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE agent_steps
		 SET error_code = 'agent_tool_failed', error_message = 'unexpected active error'
		 WHERE id = $1`,
		step.ID,
	); err == nil {
		t.Fatal("migration 39 allowed an active Step error")
	}
	definitionBeforeRejectedDown := constraintDefinition()
	if err := execScript(downSQL); err == nil {
		t.Fatal("migration 39 down accepted active decision Token usage")
	}
	if definitionAfterRejectedDown := constraintDefinition(); definitionAfterRejectedDown != definitionBeforeRejectedDown {
		t.Fatalf("constraint changed after rejected down:\nbefore=%s\nafter=%s", definitionBeforeRejectedDown, definitionAfterRejectedDown)
	}
	var fixtureStillExists bool
	if err := pool.QueryRow(
		ctx,
		`SELECT EXISTS (SELECT 1 FROM agent_steps WHERE id = $1 AND total_tokens = 5)`,
		step.ID,
	).Scan(&fixtureStillExists); err != nil || !fixtureStillExists {
		t.Fatalf("active decision fixture after rejected down = %t, %v", fixtureStillExists, err)
	}

	if _, err := pool.Exec(ctx, `DELETE FROM conversations WHERE id = $1`, conversation.ID); err != nil {
		t.Fatalf("remove active migration fixture: %v", err)
	}
	if err := execScript(downSQL); err != nil {
		t.Fatalf("safe migration 39 down: %v", err)
	}
	oldConversation := createAgentTestConversation(t, ctx, pool, owner, "migration 39 old constraint")
	queueAgentTestRun(t, ctx, repository, owner, oldConversation.ID, "migration-39-old")
	oldClaim, err := repository.ClaimNextRun(ctx)
	if err != nil {
		t.Fatalf("claim old-constraint fixture: %v", err)
	}
	if _, err := repository.PersistDecision(ctx, oldClaim.ID, oldClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"old"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); err == nil {
		t.Fatal("migration 38 constraint accepted active decision Token usage after down")
	}
	if err := execScript(upSQL); err != nil {
		t.Fatalf("migration 39 up after safe down: %v", err)
	}
	if _, err := repository.PersistDecision(ctx, oldClaim.ID, oldClaim.LeaseToken, agentapplication.PersistDecisionInput{
		ToolName: "search_documents", Arguments: json.RawMessage(`{"query":"restored"}`),
		PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); err != nil {
		t.Fatalf("migration 39 restored active decision Token usage: %v", err)
	}
}
