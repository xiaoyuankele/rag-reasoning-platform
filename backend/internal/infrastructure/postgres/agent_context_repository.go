package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type AgentContextRepository struct {
	pool          *pgxpool.Pool
	conversations *ConversationRepository
}

func NewAgentContextRepository(pool *pgxpool.Pool) *AgentContextRepository {
	return &AgentContextRepository{
		pool:          pool,
		conversations: NewConversationRepository(pool),
	}
}

func (r *AgentContextRepository) LoadFrozenAgentContext(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	userMessageID int64,
	historyLimit int,
) (conversationdomain.Message, []conversationdomain.Message, error) {
	if !scope.IsValid() {
		return conversationdomain.Message{}, nil, accessdomain.ErrInvalidOwnerScope
	}
	if conversationID <= 0 || userMessageID <= 0 || historyLimit <= 0 {
		return conversationdomain.Message{}, nil, conversationdomain.ErrNotFound
	}
	current, err := scanConversationMessage(r.pool.QueryRow(
		ctx,
		`SELECT message.id, message.conversation_id, message.sequence_no,
		        message.role, message.content, message.status,
		        message.client_message_id, message.created_at, message.updated_at
		 FROM conversations AS conversation
		 JOIN conversation_messages AS message
		   ON message.id = $3
		  AND message.conversation_id = conversation.id
		  AND message.role = 'user'
		  AND message.status = 'completed'
		 WHERE conversation.id = $1
		   AND conversation.owner_user_id = $2`,
		conversationID,
		scope.OwnerUserID(),
		userMessageID,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Message{}, nil, conversationdomain.ErrNotFound
	}
	if err != nil {
		return conversationdomain.Message{}, nil, fmt.Errorf("read frozen agent current question: %w", err)
	}
	history, err := r.conversations.ListCompletedMessagesBefore(
		ctx,
		scope,
		conversationID,
		userMessageID,
		historyLimit,
	)
	if err != nil {
		return conversationdomain.Message{}, nil, fmt.Errorf("read frozen agent history: %w", err)
	}
	if history == nil {
		history = make([]conversationdomain.Message, 0)
	}
	return current, history, nil
}
