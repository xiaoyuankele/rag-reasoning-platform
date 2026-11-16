package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestAgentContextRepositoryFreezesCurrentQuestionAndCompletedHistory(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	owner := answerJobTestOwnerScope(t, ctx, pool, "agent-context")
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	contextRepository := postgresrepository.NewAgentContextRepository(pool)
	conversation, err := conversationRepository.CreateConversation(
		ctx, owner, conversationdomain.CreateInput{Title: "Agent context"},
	)
	if err != nil {
		t.Fatalf("create context conversation: %v", err)
	}
	previous, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "previous question",
	})
	if err != nil {
		t.Fatalf("start previous turn: %v", err)
	}
	if _, err := conversationRepository.UpdateAssistantMessage(ctx, owner, conversationdomain.UpdateAssistantMessageInput{
		ConversationID: conversation.ID, MessageID: previous.AssistantMessage.ID,
		Content: "previous answer", Status: conversationdomain.MessageStatusCompleted,
	}); err != nil {
		t.Fatalf("complete previous turn: %v", err)
	}
	failed, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "completed question with failed answer",
	})
	if err != nil {
		t.Fatalf("start failed turn: %v", err)
	}
	if _, err := conversationRepository.UpdateAssistantMessage(ctx, owner, conversationdomain.UpdateAssistantMessageInput{
		ConversationID: conversation.ID, MessageID: failed.AssistantMessage.ID,
		Content: "failed answer", Status: conversationdomain.MessageStatusFailed,
	}); err != nil {
		t.Fatalf("fail previous assistant: %v", err)
	}
	current, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "current question",
	})
	if err != nil {
		t.Fatalf("start current turn: %v", err)
	}
	if _, err := conversationRepository.AppendUserMessage(ctx, owner, conversationdomain.AppendUserMessageInput{
		ConversationID: conversation.ID, Content: "later completed message",
	}); err != nil {
		t.Fatalf("append later message: %v", err)
	}

	currentMessage, frozenHistory, err := contextRepository.LoadFrozenAgentContext(
		ctx, owner, conversation.ID, current.UserMessage.ID, 20,
	)
	if err != nil {
		t.Fatalf("LoadFrozenAgentContext() error = %v", err)
	}
	if currentMessage.ID != current.UserMessage.ID || currentMessage.Content != "current question" {
		t.Fatalf("current message = %+v", currentMessage)
	}
	want := []string{"previous question", "previous answer", "completed question with failed answer"}
	if len(frozenHistory) != len(want) {
		t.Fatalf("history = %+v, want %d messages", frozenHistory, len(want))
	}
	for index, content := range want {
		if frozenHistory[index].Content != content || frozenHistory[index].SequenceNo >= current.UserMessage.SequenceNo {
			t.Fatalf("history[%d] = %+v", index, frozenHistory[index])
		}
	}
	provider, err := agentapplication.NewRuntimeContextProvider(
		contextRepository,
		answerapplication.ConversationHistoryConfig{MaxTurns: 1, MaxTokens: 1000, InputTokenBudget: 4000},
	)
	if err != nil {
		t.Fatalf("NewRuntimeContextProvider() error = %v", err)
	}
	contextSnapshot, err := provider.LoadAgentContext(ctx, owner, agentRunForContext(owner.OwnerUserID(), conversation.ID, current.UserMessage.ID))
	if err != nil || contextSnapshot.CurrentQuestion != "current question" || len(contextSnapshot.History) == 0 {
		t.Fatalf("runtime context = %+v, %v", contextSnapshot, err)
	}
	emptyConversation, err := conversationRepository.CreateConversation(
		ctx, owner, conversationdomain.CreateInput{Title: "No history"},
	)
	if err != nil {
		t.Fatalf("create empty-history conversation: %v", err)
	}
	emptyTurn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: emptyConversation.ID, Content: "first question",
	})
	if err != nil {
		t.Fatalf("start empty-history turn: %v", err)
	}
	emptyCurrent, emptyHistory, err := contextRepository.LoadFrozenAgentContext(
		ctx, owner, emptyConversation.ID, emptyTurn.UserMessage.ID, 10,
	)
	if err != nil || emptyHistory == nil || len(emptyHistory) != 0 || emptyCurrent.Content != "first question" {
		t.Fatalf("empty frozen context = current:%+v history:%+v, %v", emptyCurrent, emptyHistory, err)
	}
}

func TestAgentContextRepositoryHidesOwnerAndBoundaryMismatch(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerA := answerJobTestOwnerScope(t, ctx, pool, "agent-context-a")
	ownerB := answerJobTestOwnerScope(t, ctx, pool, "agent-context-b")
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	contextRepository := postgresrepository.NewAgentContextRepository(pool)
	conversation, err := conversationRepository.CreateConversation(
		ctx, ownerA, conversationdomain.CreateInput{Title: "Private context"},
	)
	if err != nil {
		t.Fatalf("create private conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "private question",
	})
	if err != nil {
		t.Fatalf("start private turn: %v", err)
	}
	otherConversation, err := conversationRepository.CreateConversation(
		ctx, ownerA, conversationdomain.CreateInput{Title: "Other context"},
	)
	if err != nil {
		t.Fatalf("create other conversation: %v", err)
	}
	otherTurn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: otherConversation.ID, Content: "other question",
	})
	if err != nil {
		t.Fatalf("start other turn: %v", err)
	}
	for name, scopeAndMessage := range map[string]struct {
		ownerID bool
		message int64
	}{
		"cross owner":                 {ownerID: true, message: turn.UserMessage.ID},
		"assistant boundary":          {message: turn.AssistantMessage.ID},
		"other conversation boundary": {message: otherTurn.UserMessage.ID},
		"missing boundary":            {message: 999999},
	} {
		t.Run(name, func(t *testing.T) {
			scope := ownerA
			if scopeAndMessage.ownerID {
				scope = ownerB
			}
			if _, _, err := contextRepository.LoadFrozenAgentContext(
				ctx, scope, conversation.ID, scopeAndMessage.message, 10,
			); !errors.Is(err, conversationdomain.ErrNotFound) {
				t.Fatalf("boundary error = %v, want ErrNotFound", err)
			}
		})
	}
	if _, err := pool.Exec(
		ctx,
		`UPDATE conversation_messages SET status = 'generating' WHERE id = $1`,
		turn.UserMessage.ID,
	); err != nil {
		t.Fatalf("make current message non-completed: %v", err)
	}
	if _, _, err := contextRepository.LoadFrozenAgentContext(
		ctx, ownerA, conversation.ID, turn.UserMessage.ID, 10,
	); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("non-completed current boundary error = %v", err)
	}
}

func agentRunForContext(ownerID, conversationID, userMessageID int64) agentdomain.Run {
	return agentdomain.Run{
		ID: 1, OwnerUserID: ownerID, ConversationID: conversationID, UserMessageID: userMessageID,
		ResponseLanguage: agentdomain.ResponseLanguageAuto,
	}
}
