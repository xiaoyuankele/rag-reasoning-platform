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

// ConversationRepository 按 OwnerScope 持久化个人用户会话和消息。
// 所有面向用户的查询都在 SQL 中同时约束 conversation_id 与 owner_user_id。
type ConversationRepository struct {
	pool *pgxpool.Pool
}

var _ conversationdomain.Repository = (*ConversationRepository)(nil)

func NewConversationRepository(pool *pgxpool.Pool) *ConversationRepository {
	return &ConversationRepository{pool: pool}
}

func (r *ConversationRepository) CreateConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.CreateInput,
) (conversationdomain.Conversation, error) {
	if !scope.IsValid() {
		return conversationdomain.Conversation{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		INSERT INTO conversations (owner_user_id, title)
		VALUES ($1, $2)
		RETURNING id, owner_user_id, title, status, last_message_at, created_at, updated_at
	`
	created, err := scanConversation(r.pool.QueryRow(ctx, query, scope.OwnerUserID(), input.Title))
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("create conversation: %w", err)
	}
	return created, nil
}

func (r *ConversationRepository) ListConversations(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options conversationdomain.ListOptions,
) (conversationdomain.ConversationListResult, error) {
	if !scope.IsValid() {
		return conversationdomain.ConversationListResult{}, accessdomain.ErrInvalidOwnerScope
	}

	var total int64
	if err := r.pool.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM conversations WHERE owner_user_id = $1`,
		scope.OwnerUserID(),
	).Scan(&total); err != nil {
		return conversationdomain.ConversationListResult{}, fmt.Errorf("count conversations: %w", err)
	}

	const query = `
		SELECT id, owner_user_id, title, status, last_message_at, created_at, updated_at
		FROM conversations
		WHERE owner_user_id = $1
		ORDER BY updated_at DESC, id DESC
		LIMIT $2 OFFSET $3
	`
	rows, err := r.pool.Query(ctx, query, scope.OwnerUserID(), options.Limit, options.Offset)
	if err != nil {
		return conversationdomain.ConversationListResult{}, fmt.Errorf("list conversations: %w", err)
	}
	defer rows.Close()

	conversations := make([]conversationdomain.Conversation, 0)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return conversationdomain.ConversationListResult{}, fmt.Errorf("scan conversation: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return conversationdomain.ConversationListResult{}, fmt.Errorf("iterate conversations: %w", err)
	}
	return conversationdomain.ConversationListResult{Conversations: conversations, Total: total}, nil
}

func (r *ConversationRepository) ListConversationsCursor(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	options conversationdomain.ConversationCursorOptions,
) (conversationdomain.ConversationCursorResult, error) {
	if !scope.IsValid() {
		return conversationdomain.ConversationCursorResult{}, accessdomain.ErrInvalidOwnerScope
	}
	fetchLimit := options.Limit + 1
	var rows pgx.Rows
	var err error
	if options.Cursor == nil {
		rows, err = r.pool.Query(
			ctx,
			`SELECT id, owner_user_id, title, status, last_message_at, created_at, updated_at
			 FROM conversations
			 WHERE owner_user_id = $1
			 ORDER BY updated_at DESC, id DESC
			 LIMIT $2`,
			scope.OwnerUserID(),
			fetchLimit,
		)
	} else {
		rows, err = r.pool.Query(
			ctx,
			`SELECT id, owner_user_id, title, status, last_message_at, created_at, updated_at
			 FROM conversations
			 WHERE owner_user_id = $1
			   AND (updated_at < $3 OR (updated_at = $3 AND id < $4))
			 ORDER BY updated_at DESC, id DESC
			 LIMIT $2`,
			scope.OwnerUserID(),
			fetchLimit,
			options.Cursor.UpdatedAt,
			options.Cursor.ID,
		)
	}
	if err != nil {
		return conversationdomain.ConversationCursorResult{}, fmt.Errorf("query conversation cursor page: %w", err)
	}
	defer rows.Close()
	conversations := make([]conversationdomain.Conversation, 0, fetchLimit)
	for rows.Next() {
		conversation, err := scanConversation(rows)
		if err != nil {
			return conversationdomain.ConversationCursorResult{}, fmt.Errorf("scan conversation cursor page: %w", err)
		}
		conversations = append(conversations, conversation)
	}
	if err := rows.Err(); err != nil {
		return conversationdomain.ConversationCursorResult{}, fmt.Errorf("iterate conversation cursor page: %w", err)
	}
	hasMore := int64(len(conversations)) > options.Limit
	if hasMore {
		conversations = conversations[:options.Limit]
	}
	return conversationdomain.ConversationCursorResult{Conversations: conversations, HasMore: hasMore}, nil
}

func (r *ConversationRepository) GetConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (conversationdomain.Conversation, error) {
	if !scope.IsValid() {
		return conversationdomain.Conversation{}, accessdomain.ErrInvalidOwnerScope
	}

	const query = `
		SELECT id, owner_user_id, title, status, last_message_at, created_at, updated_at
		FROM conversations
		WHERE id = $1 AND owner_user_id = $2
	`
	found, err := scanConversation(r.pool.QueryRow(ctx, query, conversationID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Conversation{}, conversationdomain.ErrNotFound
	}
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("get conversation: %w", err)
	}
	return found, nil
}

func (r *ConversationRepository) UpdateConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.UpdateInput,
) (conversationdomain.Conversation, error) {
	if !scope.IsValid() {
		return conversationdomain.Conversation{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("begin conversation update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const lockQuery = `
		SELECT id, owner_user_id, title, status, last_message_at, created_at, updated_at
		FROM conversations
		WHERE id = $1 AND owner_user_id = $2
		FOR UPDATE
	`
	locked, err := scanConversation(tx.QueryRow(ctx, lockQuery, input.ConversationID, scope.OwnerUserID()))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Conversation{}, conversationdomain.ErrNotFound
	}
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("lock scoped conversation for update: %w", err)
	}

	if input.Status != nil && *input.Status == conversationdomain.StatusArchived {
		var activeAnswerExists bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1
				FROM answer_jobs
				WHERE conversation_id = $1
				  AND owner_user_id = $2
				  AND status IN ('queued', 'processing')
			)`,
			locked.ID,
			scope.OwnerUserID(),
		).Scan(&activeAnswerExists); err != nil {
			return conversationdomain.Conversation{}, fmt.Errorf("check active answer job before archive: %w", err)
		}
		if activeAnswerExists {
			return conversationdomain.Conversation{}, conversationdomain.ErrAnswerActive
		}

		if _, err := tx.Exec(
			ctx,
			`UPDATE conversation_messages AS message
			 SET content = $2,
			     status = 'interrupted',
			     updated_at = CURRENT_TIMESTAMP
			 WHERE message.conversation_id = $1
			   AND message.role = 'assistant'
			   AND message.status = 'generating'
			   AND NOT EXISTS (
				SELECT 1
				FROM answer_jobs AS job
				WHERE job.assistant_message_id = message.id
			   )`,
			locked.ID,
			conversationdomain.AssistantInterruptedBeforeQueueText,
		); err != nil {
			return conversationdomain.Conversation{}, fmt.Errorf("interrupt orphan generating assistants before archive: %w", err)
		}
	}

	var status *string
	if input.Status != nil {
		value := string(*input.Status)
		status = &value
	}
	const query = `
		UPDATE conversations
		SET title = COALESCE($3::TEXT, title),
		    status = COALESCE($4::TEXT, status),
		    updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND owner_user_id = $2
		RETURNING id, owner_user_id, title, status, last_message_at, created_at, updated_at
	`
	updated, err := scanConversation(tx.QueryRow(
		ctx,
		query,
		locked.ID,
		scope.OwnerUserID(),
		input.Title,
		status,
	))
	if err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("update scoped conversation: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.Conversation{}, fmt.Errorf("commit conversation update: %w", err)
	}
	return updated, nil
}

func (r *ConversationRepository) DeleteConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) error {
	if !scope.IsValid() {
		return accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin conversation deletion: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var lockedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id
		 FROM conversations
		 WHERE id = $1 AND owner_user_id = $2
		 FOR UPDATE`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&lockedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock scoped conversation for deletion: %w", err)
	}

	var activeAnswerExists bool
	if err := tx.QueryRow(
		ctx,
		`SELECT EXISTS (
			SELECT 1
			FROM answer_jobs
			WHERE conversation_id = $1
			  AND owner_user_id = $2
			  AND status IN ('queued', 'processing')
		)`,
		lockedConversationID,
		scope.OwnerUserID(),
	).Scan(&activeAnswerExists); err != nil {
		return fmt.Errorf("check active answer job before conversation deletion: %w", err)
	}
	if activeAnswerExists {
		return conversationdomain.ErrAnswerActive
	}

	commandTag, err := tx.Exec(
		ctx,
		`DELETE FROM conversations WHERE id = $1 AND owner_user_id = $2`,
		lockedConversationID,
		scope.OwnerUserID(),
	)
	if err != nil {
		return fmt.Errorf("delete scoped conversation: %w", err)
	}
	if commandTag.RowsAffected() != 1 {
		return conversationdomain.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit conversation deletion: %w", err)
	}
	return nil
}

func (r *ConversationRepository) ListMessages(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	options conversationdomain.ListOptions,
) (conversationdomain.MessageListResult, error) {
	if !scope.IsValid() {
		return conversationdomain.MessageListResult{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("begin conversation message list: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const existenceQuery = `
		SELECT id
		FROM conversations
		WHERE id = $1 AND owner_user_id = $2
		FOR KEY SHARE
	`
	var exists int64
	if err := tx.QueryRow(ctx, existenceQuery, conversationID, scope.OwnerUserID()).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.MessageListResult{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("check conversation ownership: %w", err)
	}

	var total int64
	if err := tx.QueryRow(
		ctx,
		`SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = $1`,
		conversationID,
	).Scan(&total); err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("count conversation messages: %w", err)
	}

	order := options.Order
	if order == "" {
		order = conversationdomain.MessageOrderAscending
	}
	var query string
	switch order {
	case conversationdomain.MessageOrderAscending:
		query = `
		SELECT id, conversation_id, sequence_no, role, content, status,
		       client_message_id, created_at, updated_at
		FROM conversation_messages
		WHERE conversation_id = $1
		ORDER BY sequence_no ASC, id ASC
		LIMIT $2 OFFSET $3
	`
	case conversationdomain.MessageOrderDescending:
		query = `
		SELECT id, conversation_id, sequence_no, role, content, status,
		       client_message_id, created_at, updated_at
		FROM (
			SELECT id, conversation_id, sequence_no, role, content, status,
			       client_message_id, created_at, updated_at
			FROM conversation_messages
			WHERE conversation_id = $1
			ORDER BY sequence_no DESC, id DESC
			LIMIT $2 OFFSET $3
		) AS message_page
		ORDER BY sequence_no ASC, id ASC
	`
	default:
		return conversationdomain.MessageListResult{}, fmt.Errorf("invalid conversation message order %q", order)
	}
	rows, err := tx.Query(ctx, query, conversationID, options.Limit, options.Offset)
	if err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("list conversation messages: %w", err)
	}
	defer rows.Close()

	messages := make([]conversationdomain.Message, 0)
	for rows.Next() {
		message, err := scanConversationMessage(rows)
		if err != nil {
			rows.Close()
			return conversationdomain.MessageListResult{}, fmt.Errorf("scan conversation message: %w", err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("iterate conversation messages: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.MessageListResult{}, fmt.Errorf("commit conversation message list: %w", err)
	}
	return conversationdomain.MessageListResult{Messages: messages, Total: total}, nil
}

func (r *ConversationRepository) ListMessagesCursor(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	options conversationdomain.MessageCursorOptions,
) (conversationdomain.MessageCursorResult, error) {
	if !scope.IsValid() {
		return conversationdomain.MessageCursorResult{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.MessageCursorResult{}, fmt.Errorf("begin conversation message cursor list: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var lockedConversationID int64
	if err := tx.QueryRow(
		ctx,
		`SELECT id FROM conversations WHERE id = $1 AND owner_user_id = $2 FOR KEY SHARE`,
		conversationID,
		scope.OwnerUserID(),
	).Scan(&lockedConversationID); errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.MessageCursorResult{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return conversationdomain.MessageCursorResult{}, fmt.Errorf("lock conversation for message cursor list: %w", err)
	}

	fetchLimit := options.Limit + 1
	var rows pgx.Rows
	if options.Cursor == nil {
		rows, err = tx.Query(
			ctx,
			`SELECT id, conversation_id, sequence_no, role, content, status,
			        client_message_id, created_at, updated_at
			 FROM conversation_messages
			 WHERE conversation_id = $1
			 ORDER BY sequence_no DESC, id DESC
			 LIMIT $2`,
			lockedConversationID,
			fetchLimit,
		)
	} else {
		rows, err = tx.Query(
			ctx,
			`SELECT id, conversation_id, sequence_no, role, content, status,
			        client_message_id, created_at, updated_at
			 FROM conversation_messages
			 WHERE conversation_id = $1 AND sequence_no < $3
			 ORDER BY sequence_no DESC, id DESC
			 LIMIT $2`,
			lockedConversationID,
			fetchLimit,
			options.Cursor.BeforeSequenceNo,
		)
	}
	if err != nil {
		return conversationdomain.MessageCursorResult{}, fmt.Errorf("query conversation message cursor page: %w", err)
	}
	messagesDescending := make([]conversationdomain.Message, 0, fetchLimit)
	for rows.Next() {
		message, err := scanConversationMessage(rows)
		if err != nil {
			rows.Close()
			return conversationdomain.MessageCursorResult{}, fmt.Errorf("scan conversation message cursor page: %w", err)
		}
		messagesDescending = append(messagesDescending, message)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return conversationdomain.MessageCursorResult{}, fmt.Errorf("iterate conversation message cursor page: %w", err)
	}
	hasMore := int64(len(messagesDescending)) > options.Limit
	if hasMore {
		messagesDescending = messagesDescending[:options.Limit]
	}
	for left, right := 0, len(messagesDescending)-1; left < right; left, right = left+1, right-1 {
		messagesDescending[left], messagesDescending[right] = messagesDescending[right], messagesDescending[left]
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.MessageCursorResult{}, fmt.Errorf("commit conversation message cursor list: %w", err)
	}
	return conversationdomain.MessageCursorResult{Messages: messagesDescending, HasMore: hasMore}, nil
}

func (r *ConversationRepository) AppendUserMessage(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.AppendUserMessageInput,
) (conversationdomain.AppendUserMessageResult, error) {
	if !scope.IsValid() {
		return conversationdomain.AppendUserMessageResult{}, accessdomain.ErrInvalidOwnerScope
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("begin append conversation message: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var lockedID int64
	var lockedStatus string
	lockErr := tx.QueryRow(
		ctx,
		`SELECT id, status FROM conversations WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`,
		input.ConversationID,
		scope.OwnerUserID(),
	).Scan(&lockedID, &lockedStatus)
	if errors.Is(lockErr, pgx.ErrNoRows) {
		return conversationdomain.AppendUserMessageResult{}, conversationdomain.ErrNotFound
	}
	if lockErr != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("lock conversation: %w", lockErr)
	}
	if conversationdomain.Status(lockedStatus) == conversationdomain.StatusArchived {
		return conversationdomain.AppendUserMessageResult{}, conversationdomain.ErrArchived
	}
	if conversationdomain.Status(lockedStatus) != conversationdomain.StatusActive {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("conversation %d has invalid status %q", lockedID, lockedStatus)
	}

	if input.ClientMessageID != nil {
		const existingQuery = `
			SELECT id, conversation_id, sequence_no, role, content, status,
			       client_message_id, created_at, updated_at
			FROM conversation_messages
			WHERE conversation_id = $1 AND client_message_id = $2
		`
		existing, existingErr := scanConversationMessage(tx.QueryRow(
			ctx,
			existingQuery,
			input.ConversationID,
			*input.ClientMessageID,
		))
		if existingErr == nil {
			if existing.Role != conversationdomain.RoleUser || existing.Content != input.Content {
				return conversationdomain.AppendUserMessageResult{}, conversationdomain.ErrDuplicateClientMessage
			}
			if err := tx.Commit(ctx); err != nil {
				return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("commit idempotent conversation message: %w", err)
			}
			return conversationdomain.AppendUserMessageResult{Message: existing, Created: false}, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("find idempotent conversation message: %w", existingErr)
		}
	}

	var nextSequence int64
	if err := tx.QueryRow(
		ctx,
		`SELECT COALESCE(MAX(sequence_no), 0) + 1 FROM conversation_messages WHERE conversation_id = $1`,
		input.ConversationID,
	).Scan(&nextSequence); err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("allocate conversation message sequence: %w", err)
	}

	const insertQuery = `
		INSERT INTO conversation_messages (
			conversation_id, sequence_no, role, content, status, client_message_id
		)
		VALUES ($1, $2, 'user', $3, 'completed', $4)
		RETURNING id, conversation_id, sequence_no, role, content, status,
		          client_message_id, created_at, updated_at
	`
	created, err := scanConversationMessage(tx.QueryRow(
		ctx,
		insertQuery,
		input.ConversationID,
		nextSequence,
		input.Content,
		input.ClientMessageID,
	))
	if err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("insert conversation message: %w", err)
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE conversations SET last_message_at = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
		lockedID,
		created.CreatedAt,
	); err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("update conversation activity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.AppendUserMessageResult{}, fmt.Errorf("commit conversation message: %w", err)
	}
	return conversationdomain.AppendUserMessageResult{Message: created, Created: true}, nil
}

// StartTurn 在一个事务中创建 user 消息和 assistant generating 占位消息。
// 会话行锁同时保证同一会话的 sequence_no 分配稳定。
func (r *ConversationRepository) StartTurn(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.StartTurnInput,
) (conversationdomain.StartTurnResult, error) {
	if !scope.IsValid() {
		return conversationdomain.StartTurnResult{}, accessdomain.ErrInvalidOwnerScope
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("begin start conversation turn: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	var conversationID int64
	var conversationStatus string
	if err := tx.QueryRow(
		ctx,
		`SELECT id, status FROM conversations WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`,
		input.ConversationID,
		scope.OwnerUserID(),
	).Scan(&conversationID, &conversationStatus); errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.StartTurnResult{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("lock conversation for start turn: %w", err)
	}
	if conversationdomain.Status(conversationStatus) == conversationdomain.StatusArchived {
		return conversationdomain.StartTurnResult{}, conversationdomain.ErrArchived
	}
	if conversationdomain.Status(conversationStatus) != conversationdomain.StatusActive {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("conversation %d has invalid status %q", conversationID, conversationStatus)
	}

	if input.ClientMessageID != nil {
		const existingQuery = `
			SELECT id, conversation_id, sequence_no, role, content, status,
			       client_message_id, created_at, updated_at
			FROM conversation_messages
			WHERE conversation_id = $1 AND client_message_id = $2
		`
		existing, existingErr := scanConversationMessage(tx.QueryRow(
			ctx,
			existingQuery,
			input.ConversationID,
			*input.ClientMessageID,
		))
		if existingErr == nil {
			if existing.Role != conversationdomain.RoleUser || existing.Content != input.Content {
				return conversationdomain.StartTurnResult{}, conversationdomain.ErrDuplicateClientMessage
			}

			assistant, assistantErr := scanConversationMessage(tx.QueryRow(
				ctx,
				existingQueryForAssistant,
				input.ConversationID,
				existing.SequenceNo+1,
			))
			if assistantErr == nil {
				if assistant.Role != conversationdomain.RoleAssistant {
					return conversationdomain.StartTurnResult{}, fmt.Errorf("conversation sequence %d is not assistant", assistant.SequenceNo)
				}
				if err := tx.Commit(ctx); err != nil {
					return conversationdomain.StartTurnResult{}, fmt.Errorf("commit idempotent conversation turn: %w", err)
				}
				return conversationdomain.StartTurnResult{
					UserMessage: existing, AssistantMessage: assistant, Created: false,
				}, nil
			}
			if !errors.Is(assistantErr, pgx.ErrNoRows) {
				return conversationdomain.StartTurnResult{}, fmt.Errorf("find idempotent assistant message: %w", assistantErr)
			}

			assistant, err := insertConversationMessage(
				tx,
				ctx,
				input.ConversationID,
				conversationdomain.RoleAssistant,
				conversationdomain.AssistantGeneratingPlaceholder,
				conversationdomain.MessageStatusGenerating,
				nil,
			)
			if err != nil {
				return conversationdomain.StartTurnResult{}, fmt.Errorf("insert idempotent assistant message: %w", err)
			}
			if _, err := tx.Exec(ctx,
				`UPDATE conversations SET last_message_at = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
				conversationID, assistant.CreatedAt,
			); err != nil {
				return conversationdomain.StartTurnResult{}, fmt.Errorf("update idempotent conversation activity: %w", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return conversationdomain.StartTurnResult{}, fmt.Errorf("commit idempotent conversation turn: %w", err)
			}
			return conversationdomain.StartTurnResult{UserMessage: existing, AssistantMessage: assistant, Created: false}, nil
		}
		if !errors.Is(existingErr, pgx.ErrNoRows) {
			return conversationdomain.StartTurnResult{}, fmt.Errorf("find idempotent user message: %w", existingErr)
		}
	}

	var nextSequence int64
	if err := tx.QueryRow(
		ctx,
		`SELECT COALESCE(MAX(sequence_no), 0) + 1 FROM conversation_messages WHERE conversation_id = $1`,
		input.ConversationID,
	).Scan(&nextSequence); err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("allocate conversation turn sequence: %w", err)
	}

	userMessage, err := insertConversationMessage(
		tx,
		ctx,
		input.ConversationID,
		conversationdomain.RoleUser,
		input.Content,
		conversationdomain.MessageStatusCompleted,
		input.ClientMessageID,
	)
	if err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("insert conversation user message: %w", err)
	}
	if userMessage.SequenceNo != nextSequence {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("conversation user sequence mismatch: got %d want %d", userMessage.SequenceNo, nextSequence)
	}

	assistant, err := insertConversationMessage(
		tx,
		ctx,
		input.ConversationID,
		conversationdomain.RoleAssistant,
		conversationdomain.AssistantGeneratingPlaceholder,
		conversationdomain.MessageStatusGenerating,
		nil,
	)
	if err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("insert conversation assistant placeholder: %w", err)
	}
	if assistant.SequenceNo != nextSequence+1 {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("conversation assistant sequence mismatch: got %d want %d", assistant.SequenceNo, nextSequence+1)
	}

	if _, err := tx.Exec(
		ctx,
		`UPDATE conversations SET last_message_at = $2, updated_at = CURRENT_TIMESTAMP WHERE id = $1`,
		conversationID,
		assistant.CreatedAt,
	); err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("update conversation activity: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.StartTurnResult{}, fmt.Errorf("commit conversation turn: %w", err)
	}
	return conversationdomain.StartTurnResult{UserMessage: userMessage, AssistantMessage: assistant, Created: true}, nil
}

func (r *ConversationRepository) UpdateAssistantMessage(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.UpdateAssistantMessageInput,
) (conversationdomain.Message, error) {
	if !scope.IsValid() {
		return conversationdomain.Message{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("begin assistant message update: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	var ownerConversationID int64
	if err := tx.QueryRow(ctx,
		`SELECT id FROM conversations WHERE id = $1 AND owner_user_id = $2 FOR UPDATE`,
		input.ConversationID, scope.OwnerUserID(),
	).Scan(&ownerConversationID); errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Message{}, conversationdomain.ErrNotFound
	} else if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("lock conversation for assistant update: %w", err)
	}
	const query = `
		UPDATE conversation_messages
		SET content = $3, status = $4, updated_at = CURRENT_TIMESTAMP
		WHERE id = $1 AND conversation_id = $2 AND role = 'assistant'
		RETURNING id, conversation_id, sequence_no, role, content, status,
		          client_message_id, created_at, updated_at
	`
	updated, err := scanConversationMessage(tx.QueryRow(ctx, query,
		input.MessageID, ownerConversationID, input.Content, string(input.Status)))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Message{}, conversationdomain.ErrNotFound
	}
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("update assistant message: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.Message{}, fmt.Errorf("commit assistant message update: %w", err)
	}
	return updated, nil
}

// FailAssistantIfUnqueued 在与 Answer Job 创建相同的会话/助手行锁下执行条件收尾。
// 若任务已经存在或占位消息已不是 generating，则保持当前消息不变。
func (r *ConversationRepository) FailAssistantIfUnqueued(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input conversationdomain.FailAssistantIfUnqueuedInput,
) (conversationdomain.Message, error) {
	if !scope.IsValid() {
		return conversationdomain.Message{}, accessdomain.ErrInvalidOwnerScope
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("begin unqueued assistant failure: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	const lockQuery = `
		SELECT message.id, message.conversation_id, message.sequence_no,
		       message.role, message.content, message.status,
		       message.client_message_id, message.created_at, message.updated_at
		FROM conversations AS conversation
		JOIN conversation_messages AS message
		  ON message.id = $2
		 AND message.conversation_id = conversation.id
		 AND message.role = 'assistant'
		WHERE conversation.id = $1
		  AND conversation.owner_user_id = $3
		FOR UPDATE OF conversation, message
	`
	message, err := scanConversationMessage(tx.QueryRow(
		ctx, lockQuery, input.ConversationID, input.MessageID, scope.OwnerUserID(),
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return conversationdomain.Message{}, conversationdomain.ErrNotFound
	}
	if err != nil {
		return conversationdomain.Message{}, fmt.Errorf("lock unqueued assistant message: %w", err)
	}

	if message.Status == conversationdomain.MessageStatusGenerating {
		var answerJobExists bool
		if err := tx.QueryRow(
			ctx,
			`SELECT EXISTS (
				SELECT 1 FROM answer_jobs WHERE assistant_message_id = $1
			)`,
			message.ID,
		).Scan(&answerJobExists); err != nil {
			return conversationdomain.Message{}, fmt.Errorf("check assistant answer job before failure: %w", err)
		}
		if !answerJobExists {
			message, err = scanConversationMessage(tx.QueryRow(
				ctx,
				`UPDATE conversation_messages
				 SET content = $2, status = 'failed', updated_at = CURRENT_TIMESTAMP
				 WHERE id = $1 AND status = 'generating'
				 RETURNING id, conversation_id, sequence_no, role, content, status,
				           client_message_id, created_at, updated_at`,
				message.ID,
				input.Content,
			))
			if err != nil {
				return conversationdomain.Message{}, fmt.Errorf("fail unqueued assistant message: %w", err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return conversationdomain.Message{}, fmt.Errorf("commit unqueued assistant failure: %w", err)
	}
	return message, nil
}

const existingQueryForAssistant = `
	SELECT id, conversation_id, sequence_no, role, content, status,
	       client_message_id, created_at, updated_at
	FROM conversation_messages
	WHERE conversation_id = $1 AND sequence_no = $2
`

func insertConversationMessage(
	tx pgx.Tx,
	ctx context.Context,
	conversationID int64,
	role conversationdomain.Role,
	content string,
	status conversationdomain.MessageStatus,
	clientMessageID *string,
) (conversationdomain.Message, error) {
	const query = `
		INSERT INTO conversation_messages (
			conversation_id, sequence_no, role, content, status, client_message_id
		)
		SELECT $1, COALESCE(MAX(sequence_no), 0) + 1, $2, $3, $4, $5
		FROM conversation_messages
		WHERE conversation_id = $1
		RETURNING id, conversation_id, sequence_no, role, content, status,
		          client_message_id, created_at, updated_at
	`
	return scanConversationMessage(tx.QueryRow(
		ctx,
		query,
		conversationID,
		string(role),
		content,
		string(status),
		clientMessageID,
	))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanConversation(row rowScanner) (conversationdomain.Conversation, error) {
	var result conversationdomain.Conversation
	var status string
	if err := row.Scan(
		&result.ID,
		&result.OwnerUserID,
		&result.Title,
		&status,
		&result.LastMessageAt,
		&result.CreatedAt,
		&result.UpdatedAt,
	); err != nil {
		return conversationdomain.Conversation{}, err
	}
	result.Status = conversationdomain.Status(status)
	if result.ID <= 0 || result.OwnerUserID <= 0 || !result.Status.IsValid() {
		return conversationdomain.Conversation{}, fmt.Errorf("invalid conversation row")
	}
	return result, nil
}

func scanConversationMessage(row rowScanner) (conversationdomain.Message, error) {
	var result conversationdomain.Message
	var role string
	var status string
	if err := row.Scan(
		&result.ID,
		&result.ConversationID,
		&result.SequenceNo,
		&role,
		&result.Content,
		&status,
		&result.ClientMessageID,
		&result.CreatedAt,
		&result.UpdatedAt,
	); err != nil {
		return conversationdomain.Message{}, err
	}
	result.Role = conversationdomain.Role(role)
	result.Status = conversationdomain.MessageStatus(status)
	if result.ID <= 0 || result.ConversationID <= 0 || result.SequenceNo <= 0 ||
		!result.Role.IsValid() || !result.Status.IsValid() {
		return conversationdomain.Message{}, fmt.Errorf("invalid conversation message row")
	}
	return result, nil
}
