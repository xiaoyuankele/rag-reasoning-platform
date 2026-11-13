package integration_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

type conversationHistoryIntegrationSearcher struct{}

func (conversationHistoryIntegrationSearcher) Search(
	_ context.Context,
	_ accessdomain.OwnerScope,
	input embeddingapplication.SemanticSearchInput,
) (embeddingapplication.SemanticSearchOutput, error) {
	return embeddingapplication.SemanticSearchOutput{
		Query: input.Query,
		Hits: []documentdomain.SemanticSearchHit{{
			ChunkID:      1,
			DocumentID:   2,
			OriginalName: "bridge.pdf",
			Content:      "The experiment used acceleration sensors.",
		}},
	}, nil
}

type conversationHistoryIntegrationGenerator struct {
	request generationdomain.GenerateRequest
}

func (g *conversationHistoryIntegrationGenerator) Generate(
	_ context.Context,
	request generationdomain.GenerateRequest,
) (generationdomain.GenerateResult, error) {
	g.request = request
	return generationdomain.GenerateResult{
		Text:             "It used acceleration sensors. [1]",
		PromptTokens:     320,
		CompletionTokens: 12,
		TotalTokens:      332,
	}, nil
}

type conversationHistoryIntegrationObserver struct{}

func (conversationHistoryIntegrationObserver) ObserveGenerationEvent(
	context.Context,
	answerapplication.GenerationEvent,
) {
}

func TestConversationHistoryFlowsFromPostgresIntoAnswerPrompt(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	ownerID := insertDocumentOwnerIntegrationUser(
		t,
		ctx,
		pool,
		fmt.Sprintf("conversation-history-%d@example.com", time.Now().UnixNano()),
	)
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = $1", ownerID)
	})
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}
	repository := postgresrepository.NewConversationRepository(pool)
	conversation, err := repository.CreateConversation(
		ctx,
		scope,
		conversationdomain.CreateInput{Title: "history integration"},
	)
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	previous, err := repository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID,
		Content:        "What does paper A study?",
	})
	if err != nil {
		t.Fatalf("start previous turn: %v", err)
	}
	if _, err := repository.UpdateAssistantMessage(ctx, scope, conversationdomain.UpdateAssistantMessageInput{
		ConversationID: conversation.ID,
		MessageID:      previous.AssistantMessage.ID,
		Content:        "Paper A studies bridge vibration.",
		Status:         conversationdomain.MessageStatusCompleted,
	}); err != nil {
		t.Fatalf("complete previous assistant: %v", err)
	}
	current, err := repository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID,
		Content:        "What method did it use?",
	})
	if err != nil {
		t.Fatalf("start current turn: %v", err)
	}

	generator := &conversationHistoryIntegrationGenerator{}
	service, err := answerapplication.NewServiceWithConversationHistory(
		conversationHistoryIntegrationSearcher{},
		generator,
		conversationHistoryIntegrationObserver{},
		repository,
		answerapplication.ConversationHistoryConfig{
			MaxTurns:         6,
			MaxTokens:        3_000,
			InputTokenBudget: 16_000,
		},
		"fake-generation-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("create history-aware answer service: %v", err)
	}
	if _, err := service.Answer(ctx, scope, answerapplication.Input{
		Query:          current.UserMessage.Content,
		TopK:           5,
		ConversationID: &conversation.ID,
		UserMessageID:  &current.UserMessage.ID,
	}); err != nil {
		t.Fatalf("answer with PostgreSQL history: %v", err)
	}

	for _, wanted := range []string{
		"What does paper A study?",
		"Paper A studies bridge vibration.",
		"What method did it use?",
		"The experiment used acceleration sensors.",
	} {
		if !strings.Contains(generator.request.UserPrompt, wanted) {
			t.Fatalf("prompt does not contain %q: %s", wanted, generator.request.UserPrompt)
		}
	}
	if strings.Contains(generator.request.UserPrompt, conversationdomain.AssistantGeneratingPlaceholder) {
		t.Fatal("prompt contains current generating assistant placeholder")
	}
}
