package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type frozenContextReaderFunc func(
	context.Context,
	accessdomain.OwnerScope,
	int64,
	int64,
	int,
) (conversationdomain.Message, []conversationdomain.Message, error)

func (f frozenContextReaderFunc) LoadFrozenAgentContext(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	userMessageID int64,
	historyLimit int,
) (conversationdomain.Message, []conversationdomain.Message, error) {
	return f(ctx, scope, conversationID, userMessageID, historyLimit)
}

func TestRuntimeContextProviderReusesBoundedM31History(t *testing.T) {
	scope, _ := accessdomain.NewOwnerScope(42)
	run := agentdomain.Run{
		ID: 1, OwnerUserID: scope.OwnerUserID(), ConversationID: 7, UserMessageID: 9,
		ResponseLanguage: agentdomain.ResponseLanguageAuto,
	}
	history := []conversationdomain.Message{
		{ID: 1, ConversationID: 7, SequenceNo: 1, Role: conversationdomain.RoleUser, Content: "old question", Status: conversationdomain.MessageStatusCompleted},
		{ID: 2, ConversationID: 7, SequenceNo: 2, Role: conversationdomain.RoleAssistant, Content: "old answer", Status: conversationdomain.MessageStatusCompleted},
		{ID: 3, ConversationID: 7, SequenceNo: 3, Role: conversationdomain.RoleUser, Content: "recent question", Status: conversationdomain.MessageStatusCompleted},
		{ID: 4, ConversationID: 7, SequenceNo: 4, Role: conversationdomain.RoleAssistant, Content: "recent answer", Status: conversationdomain.MessageStatusCompleted},
	}
	reader := frozenContextReaderFunc(func(
		_ context.Context,
		gotScope accessdomain.OwnerScope,
		conversationID int64,
		userMessageID int64,
		historyLimit int,
	) (conversationdomain.Message, []conversationdomain.Message, error) {
		if gotScope.OwnerUserID() != scope.OwnerUserID() || conversationID != 7 || userMessageID != 9 || historyLimit != 4 {
			t.Fatalf("reader input = scope:%+v conversation:%d message:%d limit:%d", gotScope, conversationID, userMessageID, historyLimit)
		}
		return conversationdomain.Message{
			ID: 9, ConversationID: 7, SequenceNo: 5, Role: conversationdomain.RoleUser,
			Content: "What method was used?", Status: conversationdomain.MessageStatusCompleted,
		}, history, nil
	})
	provider, err := NewRuntimeContextProvider(reader, answerapplication.ConversationHistoryConfig{
		MaxTurns: 1, MaxTokens: 1000, InputTokenBudget: 4000,
	})
	if err != nil {
		t.Fatalf("NewRuntimeContextProvider() error = %v", err)
	}
	contextSnapshot, err := provider.LoadAgentContext(t.Context(), scope, run)
	if err != nil {
		t.Fatalf("LoadAgentContext() error = %v", err)
	}
	if contextSnapshot.CurrentQuestion != "What method was used?" || len(contextSnapshot.History) != 2 ||
		contextSnapshot.History[0].Content != "recent question" ||
		contextSnapshot.History[1].Content != "recent answer" ||
		!strings.Contains(contextSnapshot.SystemInstruction, "read-only tools") {
		t.Fatalf("runtime context = %+v", contextSnapshot)
	}
}

func TestRuntimeContextProviderClassifiesBoundaryAndReaderErrors(t *testing.T) {
	scope, _ := accessdomain.NewOwnerScope(42)
	run := agentdomain.Run{ID: 1, OwnerUserID: 42, ConversationID: 7, UserMessageID: 9}
	config := answerapplication.ConversationHistoryConfig{MaxTurns: 1, MaxTokens: 1000, InputTokenBudget: 4000}
	for _, test := range []struct {
		name string
		err  error
		want error
	}{
		{name: "not found", err: conversationdomain.ErrNotFound, want: conversationdomain.ErrNotFound},
		{name: "database unavailable", err: errors.New("database unavailable"), want: ErrAgentContextUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, err := NewRuntimeContextProvider(frozenContextReaderFunc(func(
				context.Context, accessdomain.OwnerScope, int64, int64, int,
			) (conversationdomain.Message, []conversationdomain.Message, error) {
				return conversationdomain.Message{}, nil, test.err
			}), config)
			if err != nil {
				t.Fatalf("NewRuntimeContextProvider() error = %v", err)
			}
			if _, err := provider.LoadAgentContext(t.Context(), scope, run); !errors.Is(err, test.want) {
				t.Fatalf("LoadAgentContext() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRuntimeContextProviderRejectsHistoryBeyondAgentBound(t *testing.T) {
	reader := frozenContextReaderFunc(func(
		context.Context, accessdomain.OwnerScope, int64, int64, int,
	) (conversationdomain.Message, []conversationdomain.Message, error) {
		return conversationdomain.Message{}, nil, nil
	})
	if _, err := NewRuntimeContextProvider(reader, answerapplication.ConversationHistoryConfig{
		MaxTurns: maxContextHistoryMessages/2 + 1, MaxTokens: 1000, InputTokenBudget: 4000,
	}); !errors.Is(err, ErrRuntimeContextConfiguration) {
		t.Fatalf("oversized history config error = %v", err)
	}
}

func TestRuntimeContextProviderRejectsNonUserCurrentBoundary(t *testing.T) {
	scope, _ := accessdomain.NewOwnerScope(42)
	provider, err := NewRuntimeContextProvider(frozenContextReaderFunc(func(
		context.Context, accessdomain.OwnerScope, int64, int64, int,
	) (conversationdomain.Message, []conversationdomain.Message, error) {
		return conversationdomain.Message{
			ID: 9, ConversationID: 7, Role: conversationdomain.RoleAssistant,
			Content: "not a question", Status: conversationdomain.MessageStatusCompleted,
		}, nil, nil
	}), answerapplication.ConversationHistoryConfig{MaxTurns: 1, MaxTokens: 1000, InputTokenBudget: 4000})
	if err != nil {
		t.Fatalf("NewRuntimeContextProvider() error = %v", err)
	}
	if _, err := provider.LoadAgentContext(t.Context(), scope, agentdomain.Run{
		ID: 1, OwnerUserID: 42, ConversationID: 7, UserMessageID: 9,
	}); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("non-user boundary error = %v", err)
	}
}
