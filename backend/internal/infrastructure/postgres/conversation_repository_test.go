package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
)

func TestConversationRepositoryScopesMessagesAndIsIdempotent(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)

	ownerA := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("conversation-a-%d@example.com", time.Now().UnixNano()))
	ownerB := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("conversation-b-%d@example.com", time.Now().UnixNano()))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id IN ($1, $2)", ownerA, ownerB)
	})

	scopeA, err := accessdomain.NewOwnerScope(ownerA)
	if err != nil {
		t.Fatalf("create owner A scope: %v", err)
	}
	scopeB, err := accessdomain.NewOwnerScope(ownerB)
	if err != nil {
		t.Fatalf("create owner B scope: %v", err)
	}
	repository := postgresrepository.NewConversationRepository(pool)

	created, err := repository.CreateConversation(ctx, scopeA, conversationdomain.CreateInput{Title: "Research notes"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if created.OwnerUserID != ownerA || created.Status != conversationdomain.StatusActive {
		t.Fatalf("created conversation = %+v", created)
	}

	clientMessageID := "client-1"
	first, err := repository.AppendUserMessage(ctx, scopeA, conversationdomain.AppendUserMessageInput{
		ConversationID:  created.ID,
		Content:         "What does the document say?",
		ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("append first message: %v", err)
	}
	if !first.Created || first.Message.SequenceNo != 1 {
		t.Fatalf("first append = %+v, want created sequence 1", first)
	}

	repeated, err := repository.AppendUserMessage(ctx, scopeA, conversationdomain.AppendUserMessageInput{
		ConversationID:  created.ID,
		Content:         "What does the document say?",
		ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("append repeated message: %v", err)
	}
	if repeated.Created || repeated.Message.ID != first.Message.ID {
		t.Fatalf("repeated append = %+v, want same existing message", repeated)
	}

	second, err := repository.AppendUserMessage(ctx, scopeA, conversationdomain.AppendUserMessageInput{
		ConversationID: created.ID,
		Content:        "Please explain the source.",
	})
	if err != nil {
		t.Fatalf("append second message: %v", err)
	}
	if !second.Created || second.Message.SequenceNo != 2 {
		t.Fatalf("second append = %+v, want created sequence 2", second)
	}

	messages, err := repository.ListMessages(ctx, scopeA, created.ID, conversationdomain.ListOptions{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("list messages: %v", err)
	}
	if messages.Total != 2 || len(messages.Messages) != 2 {
		t.Fatalf("messages = %+v, want two messages", messages)
	}

	if _, err := repository.GetConversation(ctx, scopeB, created.ID); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner get error = %v, want ErrNotFound", err)
	}
	if _, err := repository.ListMessages(ctx, scopeB, created.ID, conversationdomain.ListOptions{Limit: 10}); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner list error = %v, want ErrNotFound", err)
	}
}

func TestConversationRepositoryRejectsConflictingClientMessageID(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	ownerID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("conversation-conflict-%d@example.com", time.Now().UnixNano()))
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
	created, err := repository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: "Conflict"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	clientMessageID := "same-client-id"
	if _, err := repository.AppendUserMessage(ctx, scope, conversationdomain.AppendUserMessageInput{
		ConversationID: created.ID, Content: "first", ClientMessageID: &clientMessageID,
	}); err != nil {
		t.Fatalf("append first message: %v", err)
	}
	_, err = repository.AppendUserMessage(ctx, scope, conversationdomain.AppendUserMessageInput{
		ConversationID: created.ID, Content: "different", ClientMessageID: &clientMessageID,
	})
	if !errors.Is(err, conversationdomain.ErrDuplicateClientMessage) {
		t.Fatalf("conflicting append error = %v, want ErrDuplicateClientMessage", err)
	}
}

func TestConversationRepositoryListsNewestPagesInAscendingDisplayOrder(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewConversationRepository(pool)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("message-order-a-%d@example.com", time.Now().UnixNano()))
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("message-order-b-%d@example.com", time.Now().UnixNano()))
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	created, err := repository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "message order"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	for index := 1; index <= 6; index++ {
		if _, err := repository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
			ConversationID: created.ID,
			Content:        fmt.Sprintf("message-%d", index),
		}); err != nil {
			t.Fatalf("append message %d: %v", index, err)
		}
	}

	assertPage := func(name string, options conversationdomain.ListOptions, want []int64) {
		t.Helper()
		result, err := repository.ListMessages(ctx, ownerA, created.ID, options)
		if err != nil {
			t.Fatalf("%s list messages: %v", name, err)
		}
		if result.Total != 6 || len(result.Messages) != len(want) {
			t.Fatalf("%s result = %+v, want total 6 and %d messages", name, result, len(want))
		}
		for index, sequenceNo := range want {
			if result.Messages[index].SequenceNo != sequenceNo {
				t.Fatalf("%s sequence[%d] = %d, want %d", name, index, result.Messages[index].SequenceNo, sequenceNo)
			}
		}
	}

	assertPage("default asc", conversationdomain.ListOptions{Limit: 2}, []int64{1, 2})
	assertPage("desc page 1", conversationdomain.ListOptions{
		Limit: 2, Order: conversationdomain.MessageOrderDescending,
	}, []int64{5, 6})
	assertPage("desc page 2", conversationdomain.ListOptions{
		Limit: 2, Offset: 2, Order: conversationdomain.MessageOrderDescending,
	}, []int64{3, 4})
	assertPage("desc page 3", conversationdomain.ListOptions{
		Limit: 2, Offset: 4, Order: conversationdomain.MessageOrderDescending,
	}, []int64{1, 2})

	if _, err := repository.ListMessages(ctx, ownerB, created.ID, conversationdomain.ListOptions{
		Limit: 2, Order: conversationdomain.MessageOrderDescending,
	}); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner descending list error = %v, want ErrNotFound", err)
	}
}

func TestConversationRepositoryCursorPagination(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	repository := postgresrepository.NewConversationRepository(pool)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("cursor-a-%d@example.com", time.Now().UnixNano()))
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("cursor-b-%d@example.com", time.Now().UnixNano()))
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)

	conversations := make([]conversationdomain.Conversation, 0, 5)
	for index := 1; index <= 5; index++ {
		created, err := repository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: fmt.Sprintf("cursor-%d", index)})
		if err != nil {
			t.Fatalf("create cursor conversation %d: %v", index, err)
		}
		conversations = append(conversations, created)
	}
	baseTime := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	times := []time.Time{baseTime, baseTime, baseTime.Add(time.Second), baseTime.Add(time.Second), baseTime.Add(2 * time.Second)}
	for index, conversation := range conversations {
		if _, err := pool.Exec(ctx, `UPDATE conversations SET updated_at = $2 WHERE id = $1`, conversation.ID, times[index]); err != nil {
			t.Fatalf("set cursor tuple %d: %v", index, err)
		}
		conversations[index].UpdatedAt = times[index]
	}

	first, err := repository.ListConversationsCursor(ctx, ownerA, conversationdomain.ConversationCursorOptions{Limit: 2})
	if err != nil || !first.HasMore || len(first.Conversations) != 2 ||
		first.Conversations[0].ID != conversations[4].ID || first.Conversations[1].ID != conversations[3].ID {
		t.Fatalf("first conversation cursor page = %+v, %v", first, err)
	}
	second, err := repository.ListConversationsCursor(ctx, ownerA, conversationdomain.ConversationCursorOptions{
		Limit: 2,
		Cursor: &conversationdomain.ConversationCursor{
			UpdatedAt: first.Conversations[1].UpdatedAt, ID: first.Conversations[1].ID,
		},
	})
	if err != nil || !second.HasMore || len(second.Conversations) != 2 ||
		second.Conversations[0].ID != conversations[2].ID || second.Conversations[1].ID != conversations[1].ID {
		t.Fatalf("second conversation cursor page = %+v, %v", second, err)
	}
	third, err := repository.ListConversationsCursor(ctx, ownerA, conversationdomain.ConversationCursorOptions{
		Limit: 2,
		Cursor: &conversationdomain.ConversationCursor{
			UpdatedAt: second.Conversations[1].UpdatedAt, ID: second.Conversations[1].ID,
		},
	})
	if err != nil || third.HasMore || len(third.Conversations) != 1 || third.Conversations[0].ID != conversations[0].ID {
		t.Fatalf("third conversation cursor page = %+v, %v", third, err)
	}
	otherOwner, err := repository.ListConversationsCursor(ctx, ownerB, conversationdomain.ConversationCursorOptions{Limit: 2})
	if err != nil || len(otherOwner.Conversations) != 0 || otherOwner.HasMore {
		t.Fatalf("cross-owner conversation cursor page = %+v, %v", otherOwner, err)
	}

	messageConversation, err := repository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "message cursor"})
	if err != nil {
		t.Fatalf("create message cursor conversation: %v", err)
	}
	for index := 1; index <= 6; index++ {
		if _, err := repository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
			ConversationID: messageConversation.ID, Content: fmt.Sprintf("message-%d", index),
		}); err != nil {
			t.Fatalf("append cursor message %d: %v", index, err)
		}
	}
	messageFirst, err := repository.ListMessagesCursor(ctx, ownerA, messageConversation.ID, conversationdomain.MessageCursorOptions{Limit: 2})
	if err != nil || !messageFirst.HasMore || len(messageFirst.Messages) != 2 ||
		messageFirst.Messages[0].SequenceNo != 5 || messageFirst.Messages[1].SequenceNo != 6 {
		t.Fatalf("first message cursor page = %+v, %v", messageFirst, err)
	}
	if _, err := repository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
		ConversationID: messageConversation.ID, Content: "message-7",
	}); err != nil {
		t.Fatalf("append message after first cursor page: %v", err)
	}
	messageSecond, err := repository.ListMessagesCursor(ctx, ownerA, messageConversation.ID, conversationdomain.MessageCursorOptions{
		Limit: 2, Cursor: &conversationdomain.MessageCursor{BeforeSequenceNo: messageFirst.Messages[0].SequenceNo},
	})
	if err != nil || !messageSecond.HasMore || len(messageSecond.Messages) != 2 ||
		messageSecond.Messages[0].SequenceNo != 3 || messageSecond.Messages[1].SequenceNo != 4 {
		t.Fatalf("second message cursor page = %+v, %v", messageSecond, err)
	}
	messageThird, err := repository.ListMessagesCursor(ctx, ownerA, messageConversation.ID, conversationdomain.MessageCursorOptions{
		Limit: 2, Cursor: &conversationdomain.MessageCursor{BeforeSequenceNo: messageSecond.Messages[0].SequenceNo},
	})
	if err != nil || messageThird.HasMore || len(messageThird.Messages) != 2 ||
		messageThird.Messages[0].SequenceNo != 1 || messageThird.Messages[1].SequenceNo != 2 {
		t.Fatalf("third message cursor page = %+v, %v", messageThird, err)
	}
	if _, err := repository.ListMessagesCursor(ctx, ownerB, messageConversation.ID, conversationdomain.MessageCursorOptions{Limit: 2}); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner message cursor error = %v, want ErrNotFound", err)
	}
}

func TestConversationRepositoryManagesArchiveAndDeleteLifecycle(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("lifecycle-a-%d@example.com", time.Now().UnixNano()))
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("lifecycle-b-%d@example.com", time.Now().UnixNano()))
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	created, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "Lifecycle"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	renamed := "Renamed"
	archived := conversationdomain.StatusArchived
	active := conversationdomain.StatusActive
	updated, err := conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Title: &renamed,
	})
	if err != nil || updated.Title != renamed || updated.Status != conversationdomain.StatusActive {
		t.Fatalf("rename conversation = %+v, %v", updated, err)
	}
	if _, err := conversationRepository.UpdateConversation(ctx, ownerB, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	}); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner update error = %v, want ErrNotFound", err)
	}
	if err := conversationRepository.DeleteConversation(ctx, ownerB, created.ID); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner delete error = %v, want ErrNotFound", err)
	}

	orphanTurn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: created.ID, Content: "orphan question",
	})
	if err != nil {
		t.Fatalf("start orphan turn: %v", err)
	}
	updated, err = conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	})
	if err != nil || updated.Status != conversationdomain.StatusArchived {
		t.Fatalf("archive with orphan repair = %+v, %v", updated, err)
	}
	messages, err := conversationRepository.ListMessages(ctx, ownerA, created.ID, conversationdomain.ListOptions{Limit: 10})
	if err != nil || len(messages.Messages) != 2 ||
		messages.Messages[1].ID != orphanTurn.AssistantMessage.ID ||
		messages.Messages[1].Status != conversationdomain.MessageStatusInterrupted ||
		messages.Messages[1].Content != conversationdomain.AssistantInterruptedBeforeQueueText {
		t.Fatalf("orphan repair messages = %+v, %v", messages.Messages, err)
	}
	conversationID := created.ID
	orphanUserID := orphanTurn.UserMessage.ID
	orphanAssistantID := orphanTurn.AssistantMessage.ID
	if _, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
		Query: "orphan question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &orphanUserID, AssistantMessageID: &orphanAssistantID,
	}); !errors.Is(err, conversationdomain.ErrArchived) {
		t.Fatalf("queue archived conversation error = %v, want ErrArchived", err)
	}
	if _, err := conversationRepository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
		ConversationID: created.ID, Content: "blocked append",
	}); !errors.Is(err, conversationdomain.ErrArchived) {
		t.Fatalf("archived append error = %v, want ErrArchived", err)
	}
	if _, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: created.ID, Content: "blocked turn",
	}); !errors.Is(err, conversationdomain.ErrArchived) {
		t.Fatalf("archived start turn error = %v, want ErrArchived", err)
	}

	updated, err = conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &active,
	})
	if err != nil || updated.Status != conversationdomain.StatusActive {
		t.Fatalf("unarchive conversation = %+v, %v", updated, err)
	}
	if _, err := conversationRepository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
		ConversationID: created.ID, Content: "write after unarchive",
	}); err != nil {
		t.Fatalf("append after unarchive: %v", err)
	}

	createLinkedJob := func(content string) (conversationdomain.StartTurnResult, answerapplication.Job) {
		t.Helper()
		turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: content,
		})
		if err != nil {
			t.Fatalf("start %q turn: %v", content, err)
		}
		userMessageID := turn.UserMessage.ID
		assistantMessageID := turn.AssistantMessage.ID
		job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
			Query: content, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
		})
		if err != nil {
			t.Fatalf("create %q job: %v", content, err)
		}
		return turn, job
	}

	_, queuedJob := createLinkedJob("queued question")
	if _, err := conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	}); !errors.Is(err, conversationdomain.ErrAnswerActive) {
		t.Fatalf("archive queued job error = %v, want ErrAnswerActive", err)
	}
	if _, err := answerRepository.CancelAnswerJob(ctx, ownerA, queuedJob.ID); err != nil {
		t.Fatalf("cancel queued job: %v", err)
	}
	updated, err = conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	})
	if err != nil || updated.Status != conversationdomain.StatusArchived {
		t.Fatalf("archive after cancel = %+v, %v", updated, err)
	}
	if _, err := conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &active,
	}); err != nil {
		t.Fatalf("unarchive after cancel: %v", err)
	}

	_, processingJob := createLinkedJob("processing question")
	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != processingJob.ID {
		t.Fatalf("claim processing job = %+v, %v", claimed, err)
	}
	if _, err := conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	}); !errors.Is(err, conversationdomain.ErrAnswerActive) {
		t.Fatalf("archive processing job error = %v, want ErrAnswerActive", err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
		Query: "processing question", Answer: "done", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources: []answerapplication.Source{}, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
	}); err != nil {
		t.Fatalf("complete processing job: %v", err)
	}
	updated, err = conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &archived,
	})
	if err != nil || updated.Status != conversationdomain.StatusArchived {
		t.Fatalf("archive after terminal job = %+v, %v", updated, err)
	}
	if _, err := conversationRepository.UpdateConversation(ctx, ownerA, conversationdomain.UpdateInput{
		ConversationID: created.ID, Status: &active,
	}); err != nil {
		t.Fatalf("unarchive after terminal job: %v", err)
	}

	_, deleteBlockedJob := createLinkedJob("delete blocked")
	if err := conversationRepository.DeleteConversation(ctx, ownerA, created.ID); !errors.Is(err, conversationdomain.ErrAnswerActive) {
		t.Fatalf("delete active job error = %v, want ErrAnswerActive", err)
	}
	if _, err := answerRepository.CancelAnswerJob(ctx, ownerA, deleteBlockedJob.ID); err != nil {
		t.Fatalf("cancel delete-blocking job: %v", err)
	}
	if err := conversationRepository.DeleteConversation(ctx, ownerA, created.ID); err != nil {
		t.Fatalf("delete terminal conversation: %v", err)
	}
	for table, query := range map[string]string{
		"conversation": `SELECT COUNT(*) FROM conversations WHERE id = $1`,
		"messages":     `SELECT COUNT(*) FROM conversation_messages WHERE conversation_id = $1`,
		"answer jobs":  `SELECT COUNT(*) FROM answer_jobs WHERE conversation_id = $1`,
	} {
		var count int
		if err := pool.QueryRow(ctx, query, created.ID).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s cascade count/error = %d / %v", table, count, err)
		}
	}
}

func TestConversationDeleteSerializesWithAnswerJobCreation(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 20, MaxQueuedJobsGlobal: 100},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("delete-race-%d@example.com", time.Now().UnixNano()))
	scope, _ := accessdomain.NewOwnerScope(ownerID)

	for iteration := range 8 {
		created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: fmt.Sprintf("race %d", iteration)})
		if err != nil {
			t.Fatalf("create race conversation %d: %v", iteration, err)
		}
		turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: "race question",
		})
		if err != nil {
			t.Fatalf("start race turn %d: %v", iteration, err)
		}
		conversationID := created.ID
		userMessageID := turn.UserMessage.ID
		assistantMessageID := turn.AssistantMessage.ID
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		var deleteErr error
		var createErr error
		var createdJob answerapplication.Job
		go func() {
			defer done.Done()
			<-start
			deleteErr = conversationRepository.DeleteConversation(ctx, scope, conversationID)
		}()
		go func() {
			defer done.Done()
			<-start
			createdJob, createErr = answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
				Query: "race question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
			})
		}()
		close(start)
		done.Wait()

		switch {
		case deleteErr == nil:
			if !errors.Is(createErr, conversationdomain.ErrNotFound) {
				t.Fatalf("iteration %d delete won but create error = %v, want conversation ErrNotFound", iteration, createErr)
			}
		case createErr == nil:
			if !errors.Is(deleteErr, conversationdomain.ErrAnswerActive) {
				t.Fatalf("iteration %d create won but delete error = %v", iteration, deleteErr)
			}
			if _, err := answerRepository.CancelAnswerJob(ctx, scope, createdJob.ID); err != nil {
				t.Fatalf("cancel race job %d: %v", iteration, err)
			}
			if err := conversationRepository.DeleteConversation(ctx, scope, conversationID); err != nil {
				t.Fatalf("cleanup race conversation %d: %v", iteration, err)
			}
		default:
			t.Fatalf("iteration %d invalid race result delete=%v create=%v job=%+v", iteration, deleteErr, createErr, createdJob)
		}
	}
}

func TestConversationArchiveSerializesWithAnswerJobCreation(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 20, MaxQueuedJobsGlobal: 100},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("archive-race-%d@example.com", time.Now().UnixNano()))
	scope, _ := accessdomain.NewOwnerScope(ownerID)
	archived := conversationdomain.StatusArchived

	for iteration := range 8 {
		created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: fmt.Sprintf("archive race %d", iteration)})
		if err != nil {
			t.Fatalf("create race conversation %d: %v", iteration, err)
		}
		turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: "race question",
		})
		if err != nil {
			t.Fatalf("start race turn %d: %v", iteration, err)
		}
		conversationID := created.ID
		userMessageID := turn.UserMessage.ID
		assistantMessageID := turn.AssistantMessage.ID
		start := make(chan struct{})
		var done sync.WaitGroup
		done.Add(2)
		var archiveErr error
		var createErr error
		var createdJob answerapplication.Job
		go func() {
			defer done.Done()
			<-start
			_, archiveErr = conversationRepository.UpdateConversation(ctx, scope, conversationdomain.UpdateInput{
				ConversationID: conversationID, Status: &archived,
			})
		}()
		go func() {
			defer done.Done()
			<-start
			createdJob, createErr = answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
				Query: "race question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
			})
		}()
		close(start)
		done.Wait()

		switch {
		case archiveErr == nil:
			if !errors.Is(createErr, conversationdomain.ErrArchived) {
				t.Fatalf("iteration %d archive won but create error = %v, want ErrArchived", iteration, createErr)
			}
			messages, err := conversationRepository.ListMessages(ctx, scope, conversationID, conversationdomain.ListOptions{Limit: 10})
			if err != nil || len(messages.Messages) != 2 ||
				messages.Messages[1].ID != assistantMessageID ||
				messages.Messages[1].Status != conversationdomain.MessageStatusInterrupted ||
				messages.Messages[1].Content != conversationdomain.AssistantInterruptedBeforeQueueText {
				t.Fatalf("iteration %d archived orphan messages = %+v, %v", iteration, messages.Messages, err)
			}
		case createErr == nil:
			if !errors.Is(archiveErr, conversationdomain.ErrAnswerActive) {
				t.Fatalf("iteration %d queue won but archive error = %v", iteration, archiveErr)
			}
			if _, err := answerRepository.CancelAnswerJob(ctx, scope, createdJob.ID); err != nil {
				t.Fatalf("cancel archive race job %d: %v", iteration, err)
			}
			updated, err := conversationRepository.UpdateConversation(ctx, scope, conversationdomain.UpdateInput{
				ConversationID: conversationID, Status: &archived,
			})
			if err != nil || updated.Status != conversationdomain.StatusArchived {
				t.Fatalf("archive after race cleanup %d = %+v, %v", iteration, updated, err)
			}
		default:
			t.Fatalf("iteration %d invalid archive/create result = %v / %v, job=%+v", iteration, archiveErr, createErr, createdJob)
		}
		if err := conversationRepository.DeleteConversation(ctx, scope, conversationID); err != nil {
			t.Fatalf("delete archived race conversation %d: %v", iteration, err)
		}
	}
}

func TestConversationRepositoryReplayPreservesTerminalJobsAndRetriesMissingJob(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("conversation-replay-%d@example.com", time.Now().UnixNano()))
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}

	t.Run("queue failure without job can retry", func(t *testing.T) {
		created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: "missing job"})
		if err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		clientMessageID := "missing-job-replay"
		turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: "retry me", ClientMessageID: &clientMessageID,
		})
		if err != nil {
			t.Fatalf("start turn: %v", err)
		}
		if _, err := conversationRepository.FailAssistantIfUnqueued(ctx, scope, conversationdomain.FailAssistantIfUnqueuedInput{
			ConversationID: created.ID,
			MessageID:      turn.AssistantMessage.ID,
			Content:        "回答任务未能进入队列，请稍后重试。",
		}); err != nil {
			t.Fatalf("mark queue failure: %v", err)
		}

		replayed, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: "retry me", ClientMessageID: &clientMessageID,
		})
		if err != nil {
			t.Fatalf("replay turn: %v", err)
		}
		if replayed.Created || replayed.AssistantMessage.ID != turn.AssistantMessage.ID ||
			replayed.AssistantMessage.Status != conversationdomain.MessageStatusFailed {
			t.Fatalf("replayed missing-job turn = %+v", replayed)
		}

		conversationID := created.ID
		userMessageID := replayed.UserMessage.ID
		assistantMessageID := replayed.AssistantMessage.ID
		job, err := answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
			Query: "retry me", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
		})
		if err != nil || job.Status != answerapplication.JobStatusQueued {
			t.Fatalf("create retried linked job = %+v, %v", job, err)
		}
		messages, err := conversationRepository.ListMessages(ctx, scope, created.ID, conversationdomain.ListOptions{Limit: 10})
		if err != nil || len(messages.Messages) != 2 ||
			messages.Messages[1].Status != conversationdomain.MessageStatusGenerating ||
			messages.Messages[1].Content != conversationdomain.AssistantGeneratingPlaceholder {
			t.Fatalf("retried conversation messages = %+v, %v", messages.Messages, err)
		}
		if _, err := answerRepository.CancelAnswerJob(ctx, scope, job.ID); err != nil {
			t.Fatalf("cancel retried fixture job: %v", err)
		}
	})

	type terminalCase struct {
		name        string
		wantJob     answerapplication.JobStatus
		wantStatus  conversationdomain.MessageStatus
		wantContent string
		finalize    func(*testing.T, answerapplication.Job)
	}
	terminalCases := []terminalCase{
		{
			name:        "canceled",
			wantJob:     answerapplication.JobStatusCanceled,
			wantStatus:  conversationdomain.MessageStatusInterrupted,
			wantContent: "回答已取消。",
			finalize: func(t *testing.T, job answerapplication.Job) {
				if _, err := answerRepository.CancelAnswerJob(ctx, scope, job.ID); err != nil {
					t.Fatalf("cancel linked job: %v", err)
				}
			},
		},
		{
			name:        "failed",
			wantJob:     answerapplication.JobStatusFailed,
			wantStatus:  conversationdomain.MessageStatusFailed,
			wantContent: "暂时无法生成回答，请稍后重试。",
			finalize: func(t *testing.T, job answerapplication.Job) {
				claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
				if err != nil || claimed.ID != job.ID {
					t.Fatalf("claim linked job = %+v, %v; want %d", claimed, err, job.ID)
				}
				if err := answerRepository.MarkAnswerJobFailed(
					ctx, claimed.ID, claimed.LeaseToken,
					answerapplication.JobErrorCodeExecutionFailed, "generation failed",
				); err != nil {
					t.Fatalf("fail linked job: %v", err)
				}
			},
		},
		{
			name:        "succeeded",
			wantJob:     answerapplication.JobStatusSucceeded,
			wantStatus:  conversationdomain.MessageStatusCompleted,
			wantContent: "final answer",
			finalize: func(t *testing.T, job answerapplication.Job) {
				claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
				if err != nil || claimed.ID != job.ID {
					t.Fatalf("claim linked job = %+v, %v; want %d", claimed, err, job.ID)
				}
				if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
					Query: "question", Answer: "final answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
					Sources: []answerapplication.Source{}, PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2,
				}); err != nil {
					t.Fatalf("complete linked job: %v", err)
				}
			},
		},
	}

	for _, testCase := range terminalCases {
		t.Run(testCase.name+" job remains terminal on replay", func(t *testing.T) {
			created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: testCase.name})
			if err != nil {
				t.Fatalf("create conversation: %v", err)
			}
			clientMessageID := "terminal-" + testCase.name
			turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
				ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
			})
			if err != nil {
				t.Fatalf("start turn: %v", err)
			}
			conversationID := created.ID
			userMessageID := turn.UserMessage.ID
			assistantMessageID := turn.AssistantMessage.ID
			job, err := answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
				Query: "question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
			})
			if err != nil {
				t.Fatalf("create linked job: %v", err)
			}
			testCase.finalize(t, job)

			replayed, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
				ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
			})
			if err != nil {
				t.Fatalf("replay terminal turn: %v", err)
			}
			if replayed.Created || replayed.AssistantMessage.ID != assistantMessageID ||
				replayed.AssistantMessage.Status != testCase.wantStatus ||
				replayed.AssistantMessage.Content != testCase.wantContent {
				t.Fatalf("replayed terminal turn = %+v, want status %s content %q", replayed, testCase.wantStatus, testCase.wantContent)
			}

			replayedJob, err := answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
				Query: "question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
				ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
			})
			if err != nil || replayedJob.ID != job.ID || replayedJob.Status != testCase.wantJob {
				t.Fatalf("replayed terminal job = %+v, %v; want original %+v", replayedJob, err, job)
			}
			var jobCount int
			if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM answer_jobs WHERE user_message_id = $1`, userMessageID).Scan(&jobCount); err != nil {
				t.Fatalf("count replayed terminal jobs: %v", err)
			}
			if jobCount != 1 {
				t.Fatalf("terminal replay job count = %d, want 1", jobCount)
			}
			if testCase.name == "canceled" {
				_, insertErr := pool.Exec(
					ctx,
					`INSERT INTO answer_jobs (
						owner_user_id, conversation_id, user_message_id, assistant_message_id,
						query, top_k, requested_response_language
					 ) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
					scope.OwnerUserID(), conversationID, userMessageID, assistantMessageID,
					"duplicate", 5, answerapplication.ResponseLanguageEnglish,
				)
				var postgresError *pgconn.PgError
				if !errors.As(insertErr, &postgresError) || postgresError.ConstraintName != "uq_answer_jobs_user_message" {
					t.Fatalf("duplicate user-message insert error = %v, want uq_answer_jobs_user_message", insertErr)
				}
			}
			messages, err := conversationRepository.ListMessages(ctx, scope, created.ID, conversationdomain.ListOptions{Limit: 10})
			if err != nil || len(messages.Messages) != 2 ||
				messages.Messages[1].Status != testCase.wantStatus ||
				messages.Messages[1].Content != testCase.wantContent {
				t.Fatalf("terminal replay messages = %+v, %v", messages.Messages, err)
			}
		})
	}
}

func TestConversationAnswerReplayAndCleanupAreConcurrentSafe(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 100, MaxQueuedJobsGlobal: 200},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("conversation-race-%d@example.com", time.Now().UnixNano()))
	scope, err := accessdomain.NewOwnerScope(ownerID)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}

	t.Run("concurrent replay creates one job", func(t *testing.T) {
		created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: "concurrent replay"})
		if err != nil {
			t.Fatalf("create conversation: %v", err)
		}
		clientMessageID := "concurrent-replay"
		turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
		})
		if err != nil {
			t.Fatalf("start turn: %v", err)
		}
		if _, err := conversationRepository.FailAssistantIfUnqueued(ctx, scope, conversationdomain.FailAssistantIfUnqueuedInput{
			ConversationID: created.ID, MessageID: turn.AssistantMessage.ID, Content: "queue failed",
		}); err != nil {
			t.Fatalf("fail initial unqueued assistant: %v", err)
		}

		const callers = 8
		start := make(chan struct{})
		jobIDs := make(chan int64, callers)
		errorsCh := make(chan error, callers)
		var ready sync.WaitGroup
		var done sync.WaitGroup
		ready.Add(callers)
		done.Add(callers)
		for range callers {
			go func() {
				defer done.Done()
				ready.Done()
				<-start
				replayed, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
					ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
				})
				if err != nil {
					errorsCh <- fmt.Errorf("replay turn: %w", err)
					return
				}
				conversationID := created.ID
				userMessageID := replayed.UserMessage.ID
				assistantMessageID := replayed.AssistantMessage.ID
				job, err := answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
					Query: "question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
					ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
				})
				if err != nil {
					errorsCh <- fmt.Errorf("create replayed job: %w", err)
					return
				}
				jobIDs <- job.ID
			}()
		}
		ready.Wait()
		close(start)
		done.Wait()
		close(errorsCh)
		close(jobIDs)
		for err := range errorsCh {
			t.Error(err)
		}
		var firstJobID int64
		for jobID := range jobIDs {
			if firstJobID == 0 {
				firstJobID = jobID
			}
			if jobID != firstJobID {
				t.Fatalf("concurrent replay returned job %d and %d", firstJobID, jobID)
			}
		}
		if firstJobID == 0 {
			t.Fatal("concurrent replay returned no job")
		}
		var jobCount int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM answer_jobs WHERE user_message_id = $1`, turn.UserMessage.ID).Scan(&jobCount); err != nil {
			t.Fatalf("count concurrent replay jobs: %v", err)
		}
		if jobCount != 1 {
			t.Fatalf("concurrent replay job count = %d, want 1", jobCount)
		}
		messages, err := conversationRepository.ListMessages(ctx, scope, created.ID, conversationdomain.ListOptions{Limit: 10})
		if err != nil || len(messages.Messages) != 2 || messages.Messages[1].Status != conversationdomain.MessageStatusGenerating {
			t.Fatalf("concurrent replay messages = %+v, %v", messages.Messages, err)
		}
	})

	t.Run("cleanup and create serialize to queued generating", func(t *testing.T) {
		for iteration := range 6 {
			created, err := conversationRepository.CreateConversation(ctx, scope, conversationdomain.CreateInput{Title: fmt.Sprintf("cleanup race %d", iteration)})
			if err != nil {
				t.Fatalf("create conversation %d: %v", iteration, err)
			}
			clientMessageID := fmt.Sprintf("cleanup-race-%d", iteration)
			turn, err := conversationRepository.StartTurn(ctx, scope, conversationdomain.StartTurnInput{
				ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
			})
			if err != nil {
				t.Fatalf("start turn %d: %v", iteration, err)
			}
			conversationID := created.ID
			userMessageID := turn.UserMessage.ID
			assistantMessageID := turn.AssistantMessage.ID

			start := make(chan struct{})
			errorsCh := make(chan error, 2)
			var done sync.WaitGroup
			done.Add(2)
			go func() {
				defer done.Done()
				<-start
				_, err := conversationRepository.FailAssistantIfUnqueued(ctx, scope, conversationdomain.FailAssistantIfUnqueuedInput{
					ConversationID: conversationID, MessageID: assistantMessageID, Content: "queue failed",
				})
				if err != nil {
					errorsCh <- fmt.Errorf("cleanup assistant: %w", err)
				}
			}()
			go func() {
				defer done.Done()
				<-start
				_, err := answerRepository.CreateAnswerJob(ctx, scope, answerapplication.Input{
					Query: "question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
					ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
				})
				if err != nil {
					errorsCh <- fmt.Errorf("create answer job: %w", err)
				}
			}()
			close(start)
			done.Wait()
			close(errorsCh)
			for err := range errorsCh {
				t.Fatal(err)
			}

			job, err := answerRepository.GetAnswerJobByUserMessageID(ctx, scope, userMessageID)
			if err != nil || job.Status != answerapplication.JobStatusQueued {
				t.Fatalf("cleanup race job %d = %+v, %v", iteration, job, err)
			}
			messages, err := conversationRepository.ListMessages(ctx, scope, conversationID, conversationdomain.ListOptions{Limit: 10})
			if err != nil || len(messages.Messages) != 2 ||
				messages.Messages[1].Status != conversationdomain.MessageStatusGenerating ||
				messages.Messages[1].Content != conversationdomain.AssistantGeneratingPlaceholder {
				t.Fatalf("cleanup race messages %d = %+v, %v", iteration, messages.Messages, err)
			}
		}
	})
}

func TestAnswerJobRepositoryGetsActiveConversationJobWithinOwnerScope(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("active-a-%d@example.com", time.Now().UnixNano()))
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("active-b-%d@example.com", time.Now().UnixNano()))
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	created, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "active"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	clientMessageID := "active-job"
	turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: created.ID, Content: "question", ClientMessageID: &clientMessageID,
	})
	if err != nil {
		t.Fatalf("start turn: %v", err)
	}
	conversationID := created.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
		Query: "question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create linked job: %v", err)
	}
	protectedAssistant, err := conversationRepository.FailAssistantIfUnqueued(
		ctx,
		ownerA,
		conversationdomain.FailAssistantIfUnqueuedInput{
			ConversationID: created.ID, MessageID: assistantMessageID, Content: "must not overwrite queued job",
		},
	)
	if err != nil || protectedAssistant.Status != conversationdomain.MessageStatusGenerating ||
		protectedAssistant.Content != conversationdomain.AssistantGeneratingPlaceholder {
		t.Fatalf("job-protected assistant = %+v, %v", protectedAssistant, err)
	}

	active, err := answerRepository.GetActiveAnswerJobByConversationID(ctx, ownerA, created.ID)
	if err != nil || active.ID != job.ID || active.Status != answerapplication.JobStatusQueued {
		t.Fatalf("queued active lookup = %+v, %v", active, err)
	}
	if _, err := answerRepository.GetActiveAnswerJobByConversationID(ctx, ownerB, created.ID); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner active lookup error = %v, want conversation ErrNotFound", err)
	}

	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != job.ID {
		t.Fatalf("claim active job = %+v, %v", claimed, err)
	}
	active, err = answerRepository.GetActiveAnswerJobByConversationID(ctx, ownerA, created.ID)
	if err != nil || active.ID != job.ID || active.Status != answerapplication.JobStatusProcessing {
		t.Fatalf("processing active lookup = %+v, %v", active, err)
	}
	if err := answerRepository.MarkAnswerJobFailed(
		ctx, claimed.ID, claimed.LeaseToken,
		answerapplication.JobErrorCodeExecutionFailed, "generation failed",
	); err != nil {
		t.Fatalf("finish active job: %v", err)
	}
	if _, err := answerRepository.GetActiveAnswerJobByConversationID(ctx, ownerA, created.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("terminal active lookup error = %v, want ErrAnswerJobNotFound", err)
	}
}

func TestAnswerJobRepositoryGetsLatestConversationJobWithinOwnerScope(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	pool := openIsolatedDocumentTestPool(t, ctx)
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	ownerAID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("latest-a-%d@example.com", time.Now().UnixNano()))
	ownerBID := insertScopedRepositoryUser(t, ctx, pool, fmt.Sprintf("latest-b-%d@example.com", time.Now().UnixNano()))
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	created, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "latest"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := answerRepository.GetLatestAnswerJobByConversationID(ctx, ownerA, created.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("empty latest lookup error = %v, want ErrAnswerJobNotFound", err)
	}
	if _, err := answerRepository.GetLatestAnswerJobByConversationID(ctx, ownerB, created.ID); !errors.Is(err, conversationdomain.ErrNotFound) {
		t.Fatalf("cross-owner latest lookup error = %v, want conversation ErrNotFound", err)
	}
	var indexDefinition string
	if err := pool.QueryRow(
		ctx,
		`SELECT indexdef
		 FROM pg_indexes
		 WHERE schemaname = CURRENT_SCHEMA()
		   AND indexname = 'idx_answer_jobs_conversation_latest'`,
	).Scan(&indexDefinition); err != nil {
		t.Fatalf("find latest conversation job index: %v", err)
	}
	if !strings.Contains(indexDefinition, "conversation_id, id DESC") ||
		!strings.Contains(indexDefinition, "conversation_id IS NOT NULL") {
		t.Fatalf("latest conversation job index = %q", indexDefinition)
	}

	createLinkedJob := func(content string) answerapplication.Job {
		t.Helper()
		turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
			ConversationID: created.ID, Content: content,
		})
		if err != nil {
			t.Fatalf("start %q turn: %v", content, err)
		}
		conversationID := created.ID
		userMessageID := turn.UserMessage.ID
		assistantMessageID := turn.AssistantMessage.ID
		job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
			Query: content, TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
			ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
		})
		if err != nil {
			t.Fatalf("create %q job: %v", content, err)
		}
		return job
	}

	assertLatestStatus := func(wantID int64, wantStatus answerapplication.JobStatus) answerapplication.Job {
		t.Helper()
		latest, err := answerRepository.GetLatestAnswerJobByConversationID(ctx, ownerA, created.ID)
		if err != nil || latest.ID != wantID || latest.Status != wantStatus {
			t.Fatalf("latest job = %+v, %v; want id=%d status=%s", latest, err, wantID, wantStatus)
		}
		return latest
	}

	first := createLinkedJob("first")
	assertLatestStatus(first.ID, answerapplication.JobStatusQueued)
	claimed, err := answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != first.ID {
		t.Fatalf("claim first job = %+v, %v", claimed, err)
	}
	assertLatestStatus(first.ID, answerapplication.JobStatusProcessing)
	if err := answerRepository.MarkAnswerJobFailed(
		ctx, claimed.ID, claimed.LeaseToken,
		answerapplication.JobErrorCodeExecutionFailed, "generation failed",
	); err != nil {
		t.Fatalf("fail first job: %v", err)
	}
	assertLatestStatus(first.ID, answerapplication.JobStatusFailed)

	second := createLinkedJob("second")
	assertLatestStatus(second.ID, answerapplication.JobStatusQueued)
	if _, err := answerRepository.CancelAnswerJob(ctx, ownerA, second.ID); err != nil {
		t.Fatalf("cancel second job: %v", err)
	}
	assertLatestStatus(second.ID, answerapplication.JobStatusCanceled)

	third := createLinkedJob("third")
	assertLatestStatus(third.ID, answerapplication.JobStatusQueued)
	claimed, err = answerRepository.ClaimNextAnswerJob(ctx)
	if err != nil || claimed.ID != third.ID {
		t.Fatalf("claim third job = %+v, %v", claimed, err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, claimed.ID, claimed.LeaseToken, answerapplication.Output{
		Query: "third", Answer: "latest answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: 8, OriginalName: "source.pdf"}},
		PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
	}); err != nil {
		t.Fatalf("complete third job: %v", err)
	}

	latest := assertLatestStatus(third.ID, answerapplication.JobStatusSucceeded)
	if latest.Result == nil ||
		latest.Result.Answer != "latest answer" || latest.Result.TotalTokens != 14 ||
		len(latest.Result.Sources) != 1 || latest.Result.Sources[0].OriginalName != "source.pdf" {
		t.Fatalf("latest job = %+v, want third succeeded snapshot", latest)
	}
}
