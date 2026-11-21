package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/api"
	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	authapplication "rag-reasoning-platform/backend/internal/application/auth"
	conversationapplication "rag-reasoning-platform/backend/internal/application/conversation"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	postgresrepository "rag-reasoning-platform/backend/internal/infrastructure/postgres"
	sessioninfrastructure "rag-reasoning-platform/backend/internal/infrastructure/session"
)

type blockingConversationAnswerJobService struct {
	next    *answerapplication.JobService
	entered chan struct{}
	release chan struct{}
}

func (s *blockingConversationAnswerJobService) QueueConversation(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input answerapplication.ConversationQueueInput,
) (answerapplication.Job, error) {
	close(s.entered)
	select {
	case <-s.release:
		return s.next.QueueConversation(ctx, scope, input)
	case <-ctx.Done():
		return answerapplication.Job{}, ctx.Err()
	}
}

func (s *blockingConversationAnswerJobService) GetActiveConversationJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	return s.next.GetActiveConversationJob(ctx, scope, conversationID)
}

func (s *blockingConversationAnswerJobService) GetLatestConversationJob(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
) (answerapplication.Job, error) {
	return s.next.GetLatestConversationJob(ctx, scope, conversationID)
}

func TestConversationLatestAnswerHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-latest-a-%d@example.com", uniqueSuffix))
	ownerBID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-latest-b-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{ownerAID, ownerBID})
	})

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerAID)
	ownerBCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerBID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}

	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	answerService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewConversationAnswerHandler(conversationService, answerService, logger).RegisterRoutes(protectedRoutes)

	ownerA, err := accessdomain.NewOwnerScope(ownerAID)
	if err != nil {
		t.Fatalf("create owner scope: %v", err)
	}
	conversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "Latest HTTP"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	path := fmt.Sprintf("/conversations/%d/answer-jobs/latest", conversation.ID)

	emptyResponse := performDocumentOwnerRequest(router, http.MethodGet, path, nil, ownerACookie, "")
	if emptyResponse.Code != http.StatusNoContent || emptyResponse.Body.Len() != 0 {
		t.Fatalf("empty latest status/body = %d / %s, want 204 empty", emptyResponse.Code, emptyResponse.Body.String())
	}
	crossOwnerResponse := performDocumentOwnerRequest(router, http.MethodGet, path, nil, ownerBCookie, "")
	if crossOwnerResponse.Code != http.StatusNotFound {
		t.Fatalf("cross-owner latest status/body = %d / %s, want 404", crossOwnerResponse.Code, crossOwnerResponse.Body.String())
	}
	var crossOwnerError struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(crossOwnerResponse.Body.Bytes(), &crossOwnerError); err != nil || crossOwnerError.Code != "conversation_not_found" {
		t.Fatalf("cross-owner latest error = %+v, %v", crossOwnerError, err)
	}

	turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "latest question",
	})
	if err != nil {
		t.Fatalf("start conversation turn: %v", err)
	}
	conversationID := conversation.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
		Query: "latest question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create answer job: %v", err)
	}
	leaseToken := fmt.Sprintf("latest-http-lease-%d", uniqueSuffix)
	commandTag, err := pool.Exec(
		ctx,
		`UPDATE answer_jobs
		 SET status = 'processing',
		     attempt_count = attempt_count + 1,
		     worker_id = 'latest-http-test',
		     lease_token = $2,
		     lease_expires_at = CURRENT_TIMESTAMP + INTERVAL '1 minute',
		     started_at = CURRENT_TIMESTAMP,
		     updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'queued'`,
		job.ID,
		leaseToken,
	)
	if err != nil || commandTag.RowsAffected() != 1 {
		t.Fatalf("claim answer job directly = %d rows, %v", commandTag.RowsAffected(), err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, job.ID, leaseToken, answerapplication.Output{
		Query: "latest question", Answer: "latest answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: 8, OriginalName: "source.pdf"}},
		PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14,
	}); err != nil {
		t.Fatalf("complete answer job: %v", err)
	}

	latestResponse := performDocumentOwnerRequest(router, http.MethodGet, path, nil, ownerACookie, "")
	if latestResponse.Code != http.StatusOK {
		t.Fatalf("latest status/body = %d / %s, want 200", latestResponse.Code, latestResponse.Body.String())
	}
	var payload struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		Result struct {
			Answer  string `json:"answer"`
			Sources []struct {
				OriginalName string `json:"original_name"`
			} `json:"sources"`
			Usage struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			} `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal(latestResponse.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode latest response: %v", err)
	}
	if payload.ID != job.ID || payload.Status != string(answerapplication.JobStatusSucceeded) ||
		payload.Result.Answer != "latest answer" || len(payload.Result.Sources) != 1 ||
		payload.Result.Sources[0].OriginalName != "source.pdf" ||
		payload.Result.Usage.PromptTokens != 10 || payload.Result.Usage.CompletionTokens != 4 ||
		payload.Result.Usage.TotalTokens != 14 {
		t.Fatalf("latest response payload = %+v", payload)
	}
}

func TestConversationManagementHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-manage-a-%d@example.com", uniqueSuffix))
	ownerBID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-manage-b-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{ownerAID, ownerBID})
	})

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerAID)
	ownerBCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerBID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	answerService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewConversationHandler(conversationService, logger).RegisterRoutes(protectedRoutes)
	api.NewConversationAnswerHandler(conversationService, answerService, logger).RegisterRoutes(protectedRoutes)
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)

	assertStatusCode := func(responseCode int, body string, wantStatus int, wantCode string) {
		t.Helper()
		if responseCode != wantStatus || (wantCode != "" && !strings.Contains(body, `"code":"`+wantCode+`"`)) {
			t.Fatalf("status/body = %d / %s, want %d / %s", responseCode, body, wantStatus, wantCode)
		}
	}

	conversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "Manage HTTP"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	path := fmt.Sprintf("/conversations/%d", conversation.ID)
	crossOwnerPatch := performDocumentOwnerRequest(
		router, http.MethodPatch, path, strings.NewReader(`{"title":"forbidden"}`), ownerBCookie, "application/json",
	)
	assertStatusCode(crossOwnerPatch.Code, crossOwnerPatch.Body.String(), http.StatusNotFound, "conversation_not_found")
	crossOwnerDelete := performDocumentOwnerRequest(router, http.MethodDelete, path, nil, ownerBCookie, "")
	assertStatusCode(crossOwnerDelete.Code, crossOwnerDelete.Body.String(), http.StatusNotFound, "conversation_not_found")

	archiveResponse := performDocumentOwnerRequest(
		router, http.MethodPatch, path, strings.NewReader(`{"title":"Archived HTTP","status":"archived"}`), ownerACookie, "application/json",
	)
	assertStatusCode(archiveResponse.Code, archiveResponse.Body.String(), http.StatusOK, "")
	appendArchived := performDocumentOwnerRequest(
		router, http.MethodPost, path+"/messages", strings.NewReader(`{"content":"blocked"}`), ownerACookie, "application/json",
	)
	assertStatusCode(appendArchived.Code, appendArchived.Body.String(), http.StatusConflict, "conversation_archived")
	answerArchived := performDocumentOwnerRequest(
		router, http.MethodPost, path+"/answer-jobs", strings.NewReader(`{"content":"blocked","top_k":5,"response_language":"en"}`), ownerACookie, "application/json",
	)
	assertStatusCode(answerArchived.Code, answerArchived.Body.String(), http.StatusConflict, "conversation_archived")
	unarchiveResponse := performDocumentOwnerRequest(
		router, http.MethodPatch, path, strings.NewReader(`{"status":"active"}`), ownerACookie, "application/json",
	)
	assertStatusCode(unarchiveResponse.Code, unarchiveResponse.Body.String(), http.StatusOK, "")
	appendActive := performDocumentOwnerRequest(
		router, http.MethodPost, path+"/messages", strings.NewReader(`{"content":"restored"}`), ownerACookie, "application/json",
	)
	assertStatusCode(appendActive.Code, appendActive.Body.String(), http.StatusCreated, "")
	deleteResponse := performDocumentOwnerRequest(router, http.MethodDelete, path, nil, ownerACookie, "")
	assertStatusCode(deleteResponse.Code, deleteResponse.Body.String(), http.StatusNoContent, "")

	activeConversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "Active Job"})
	if err != nil {
		t.Fatalf("create active-job conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: activeConversation.ID, Content: "active question",
	})
	if err != nil {
		t.Fatalf("start active-job turn: %v", err)
	}
	conversationID := activeConversation.ID
	userMessageID := turn.UserMessage.ID
	assistantMessageID := turn.AssistantMessage.ID
	job, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
		Query: "active question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		ConversationID: &conversationID, UserMessageID: &userMessageID, AssistantMessageID: &assistantMessageID,
	})
	if err != nil {
		t.Fatalf("create active answer job: %v", err)
	}
	activePath := fmt.Sprintf("/conversations/%d", activeConversation.ID)
	archiveActive := performDocumentOwnerRequest(
		router, http.MethodPatch, activePath, strings.NewReader(`{"status":"archived"}`), ownerACookie, "application/json",
	)
	assertStatusCode(archiveActive.Code, archiveActive.Body.String(), http.StatusConflict, "conversation_answer_active")
	deleteActive := performDocumentOwnerRequest(router, http.MethodDelete, activePath, nil, ownerACookie, "")
	assertStatusCode(deleteActive.Code, deleteActive.Body.String(), http.StatusConflict, "conversation_answer_active")
	if _, err := answerRepository.CancelAnswerJob(ctx, ownerA, job.ID); err != nil {
		t.Fatalf("cancel active answer job: %v", err)
	}
	archiveCanceled := performDocumentOwnerRequest(
		router, http.MethodPatch, activePath, strings.NewReader(`{"status":"archived"}`), ownerACookie, "application/json",
	)
	assertStatusCode(archiveCanceled.Code, archiveCanceled.Body.String(), http.StatusOK, "")
	deleteArchived := performDocumentOwnerRequest(router, http.MethodDelete, activePath, nil, ownerACookie, "")
	assertStatusCode(deleteArchived.Code, deleteArchived.Body.String(), http.StatusNoContent, "")
}

func TestConversationAnswerHTTPReportsArchiveAndDeleteRaces(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-race-http-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = $1", ownerID)
	})

	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 5, MaxQueuedJobsGlobal: 20},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	answerService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}
	owner, _ := accessdomain.NewOwnerScope(ownerID)

	runRace := func(name string, method string, body string, wantActionStatus int, wantAnswerStatus int, wantAnswerCode string) {
		t.Helper()
		conversation, err := conversationRepository.CreateConversation(ctx, owner, conversationdomain.CreateInput{Title: name})
		if err != nil {
			t.Fatalf("create %s conversation: %v", name, err)
		}
		blocked := &blockingConversationAnswerJobService{
			next: answerService, entered: make(chan struct{}), release: make(chan struct{}),
		}
		gin.SetMode(gin.TestMode)
		router := api.NewRouter(logger)
		protectedRoutes := router.Group("")
		protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
		api.NewConversationHandler(conversationService, logger).RegisterRoutes(protectedRoutes)
		api.NewConversationAnswerHandler(conversationService, blocked, logger).RegisterRoutes(protectedRoutes)
		path := fmt.Sprintf("/conversations/%d", conversation.ID)
		answerResponse := make(chan struct {
			status int
			body   string
		}, 1)
		go func() {
			response := performDocumentOwnerRequest(
				router,
				http.MethodPost,
				path+"/answer-jobs",
				strings.NewReader(`{"content":"race question","top_k":5,"response_language":"en"}`),
				ownerCookie,
				"application/json",
			)
			answerResponse <- struct {
				status int
				body   string
			}{status: response.Code, body: response.Body.String()}
		}()

		select {
		case <-blocked.entered:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s answer request did not reach queue boundary", name)
		}
		actionResponse := performDocumentOwnerRequest(
			router,
			method,
			path,
			strings.NewReader(body),
			ownerCookie,
			func() string {
				if body == "" {
					return ""
				}
				return "application/json"
			}(),
		)
		if actionResponse.Code != wantActionStatus {
			t.Fatalf("%s action status/body = %d / %s", name, actionResponse.Code, actionResponse.Body.String())
		}
		close(blocked.release)
		var result struct {
			status int
			body   string
		}
		select {
		case result = <-answerResponse:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s answer request did not finish", name)
		}
		if result.status != wantAnswerStatus || !strings.Contains(result.body, `"code":"`+wantAnswerCode+`"`) {
			t.Fatalf("%s answer status/body = %d / %s", name, result.status, result.body)
		}
		if method == http.MethodPatch {
			if err := conversationRepository.DeleteConversation(ctx, owner, conversation.ID); err != nil {
				t.Fatalf("delete archived %s conversation: %v", name, err)
			}
		}
	}

	runRace("archive wins", http.MethodPatch, `{"status":"archived"}`, http.StatusOK, http.StatusConflict, "conversation_archived")
	runRace("delete wins", http.MethodDelete, "", http.StatusNoContent, http.StatusNotFound, "conversation_not_found")
}

func TestConversationCursorHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-cursor-a-%d@example.com", uniqueSuffix))
	ownerBID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("conversation-cursor-b-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{ownerAID, ownerBID})
	})
	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerAID)
	ownerBCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerBID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewConversationHandler(conversationService, logger).RegisterRoutes(protectedRoutes)
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)

	type cursorMetadata struct {
		Next    *string `json:"next"`
		HasMore bool    `json:"has_more"`
	}
	type conversationCursorResponse struct {
		Conversations []struct {
			ID int64 `json:"id"`
		} `json:"conversations"`
		Cursor cursorMetadata `json:"cursor"`
	}
	type messageCursorResponse struct {
		ConversationID int64 `json:"conversation_id"`
		Messages       []struct {
			SequenceNo int64 `json:"sequence_no"`
		} `json:"messages"`
		Cursor cursorMetadata `json:"cursor"`
	}

	conversations := make([]conversationdomain.Conversation, 0, 5)
	for index := 1; index <= 5; index++ {
		created, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: fmt.Sprintf("http cursor %d", index)})
		if err != nil {
			t.Fatalf("create cursor conversation %d: %v", index, err)
		}
		conversations = append(conversations, created)
	}
	baseTime := time.Now().UTC().Add(time.Minute).Truncate(time.Microsecond)
	times := []time.Time{baseTime, baseTime, baseTime.Add(time.Second), baseTime.Add(time.Second), baseTime.Add(2 * time.Second)}
	for index, conversation := range conversations {
		if _, err := pool.Exec(ctx, `UPDATE conversations SET updated_at = $2 WHERE id = $1`, conversation.ID, times[index]); err != nil {
			t.Fatalf("set HTTP cursor tuple %d: %v", index, err)
		}
	}

	readConversationPage := func(path string, cookie string) conversationCursorResponse {
		t.Helper()
		response := performDocumentOwnerRequest(router, http.MethodGet, path, nil, cookie, "")
		if response.Code != http.StatusOK {
			t.Fatalf("conversation cursor status/body = %d / %s", response.Code, response.Body.String())
		}
		var payload conversationCursorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode conversation cursor page: %v", err)
		}
		return payload
	}
	first := readConversationPage("/conversations/cursor?page_size=2", ownerACookie)
	if len(first.Conversations) != 2 || first.Conversations[0].ID != conversations[4].ID ||
		first.Conversations[1].ID != conversations[3].ID || first.Cursor.Next == nil || !first.Cursor.HasMore {
		t.Fatalf("first HTTP conversation cursor page = %+v", first)
	}
	second := readConversationPage("/conversations/cursor?page_size=2&cursor="+url.QueryEscape(*first.Cursor.Next), ownerACookie)
	if len(second.Conversations) != 2 || second.Conversations[0].ID != conversations[2].ID ||
		second.Conversations[1].ID != conversations[1].ID || second.Cursor.Next == nil || !second.Cursor.HasMore {
		t.Fatalf("second HTTP conversation cursor page = %+v", second)
	}
	third := readConversationPage("/conversations/cursor?page_size=2&cursor="+url.QueryEscape(*second.Cursor.Next), ownerACookie)
	if len(third.Conversations) != 1 || third.Conversations[0].ID != conversations[0].ID ||
		third.Cursor.Next != nil || third.Cursor.HasMore {
		t.Fatalf("third HTTP conversation cursor page = %+v", third)
	}
	otherOwner := readConversationPage("/conversations/cursor?page_size=2", ownerBCookie)
	if len(otherOwner.Conversations) != 0 || otherOwner.Cursor.Next != nil || otherOwner.Cursor.HasMore {
		t.Fatalf("cross-owner conversation cursor page = %+v", otherOwner)
	}

	messageConversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "HTTP message cursor"})
	if err != nil {
		t.Fatalf("create HTTP message cursor conversation: %v", err)
	}
	for index := 1; index <= 6; index++ {
		if _, err := conversationRepository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
			ConversationID: messageConversation.ID, Content: fmt.Sprintf("message-%d", index),
		}); err != nil {
			t.Fatalf("append HTTP cursor message %d: %v", index, err)
		}
	}
	messagePath := fmt.Sprintf("/conversations/%d/messages/cursor", messageConversation.ID)
	readMessagePage := func(path string, cookie string) (*messageCursorResponse, int, string) {
		t.Helper()
		response := performDocumentOwnerRequest(router, http.MethodGet, path, nil, cookie, "")
		if response.Code != http.StatusOK {
			return nil, response.Code, response.Body.String()
		}
		var payload messageCursorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode message cursor page: %v", err)
		}
		return &payload, response.Code, response.Body.String()
	}
	messageFirst, _, _ := readMessagePage(messagePath+"?page_size=2", ownerACookie)
	if messageFirst == nil || len(messageFirst.Messages) != 2 ||
		messageFirst.Messages[0].SequenceNo != 5 || messageFirst.Messages[1].SequenceNo != 6 ||
		messageFirst.Cursor.Next == nil || !messageFirst.Cursor.HasMore {
		t.Fatalf("first HTTP message cursor page = %+v", messageFirst)
	}
	if _, err := conversationRepository.AppendUserMessage(ctx, ownerA, conversationdomain.AppendUserMessageInput{
		ConversationID: messageConversation.ID, Content: "message-7",
	}); err != nil {
		t.Fatalf("append HTTP message after cursor: %v", err)
	}
	messageSecond, _, _ := readMessagePage(messagePath+"?page_size=2&cursor="+url.QueryEscape(*messageFirst.Cursor.Next), ownerACookie)
	if messageSecond == nil || len(messageSecond.Messages) != 2 ||
		messageSecond.Messages[0].SequenceNo != 3 || messageSecond.Messages[1].SequenceNo != 4 ||
		messageSecond.Cursor.Next == nil || !messageSecond.Cursor.HasMore {
		t.Fatalf("second HTTP message cursor page = %+v", messageSecond)
	}
	messageThird, _, _ := readMessagePage(messagePath+"?page_size=2&cursor="+url.QueryEscape(*messageSecond.Cursor.Next), ownerACookie)
	if messageThird == nil || len(messageThird.Messages) != 2 ||
		messageThird.Messages[0].SequenceNo != 1 || messageThird.Messages[1].SequenceNo != 2 ||
		messageThird.Cursor.Next != nil || messageThird.Cursor.HasMore {
		t.Fatalf("third HTTP message cursor page = %+v", messageThird)
	}

	_, crossOwnerStatus, crossOwnerBody := readMessagePage(messagePath+"?page_size=2", ownerBCookie)
	if crossOwnerStatus != http.StatusNotFound || !strings.Contains(crossOwnerBody, `"code":"conversation_not_found"`) {
		t.Fatalf("cross-owner message cursor status/body = %d / %s", crossOwnerStatus, crossOwnerBody)
	}
	wrongType := performDocumentOwnerRequest(
		router, http.MethodGet, messagePath+"?page_size=2&cursor="+url.QueryEscape(*first.Cursor.Next), nil, ownerACookie, "",
	)
	if wrongType.Code != http.StatusBadRequest || !strings.Contains(wrongType.Body.String(), `"code":"invalid_conversation_message_cursor"`) {
		t.Fatalf("cross-type message cursor status/body = %d / %s", wrongType.Code, wrongType.Body.String())
	}
	wrongConversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "wrong cursor target"})
	if err != nil {
		t.Fatalf("create wrong cursor target: %v", err)
	}
	wrongConversationResponse := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		fmt.Sprintf("/conversations/%d/messages/cursor?page_size=2&cursor=%s", wrongConversation.ID, url.QueryEscape(*messageFirst.Cursor.Next)),
		nil,
		ownerACookie,
		"",
	)
	if wrongConversationResponse.Code != http.StatusBadRequest ||
		!strings.Contains(wrongConversationResponse.Body.String(), `"code":"invalid_conversation_message_cursor"`) {
		t.Fatalf("cross-conversation cursor status/body = %d / %s", wrongConversationResponse.Code, wrongConversationResponse.Body.String())
	}
	wrongConversationCursor := performDocumentOwnerRequest(
		router, http.MethodGet, "/conversations/cursor?page_size=2&cursor="+url.QueryEscape(*messageFirst.Cursor.Next), nil, ownerACookie, "",
	)
	if wrongConversationCursor.Code != http.StatusBadRequest ||
		!strings.Contains(wrongConversationCursor.Body.String(), `"code":"invalid_conversation_cursor"`) {
		t.Fatalf("cross-type conversation cursor status/body = %d / %s", wrongConversationCursor.Code, wrongConversationCursor.Body.String())
	}
	malformed := performDocumentOwnerRequest(router, http.MethodGet, "/conversations/cursor?cursor=bad", nil, ownerACookie, "")
	if malformed.Code != http.StatusBadRequest || !strings.Contains(malformed.Body.String(), `"code":"invalid_conversation_cursor"`) {
		t.Fatalf("malformed conversation cursor status/body = %d / %s", malformed.Code, malformed.Body.String())
	}
}

func TestStandaloneAnswerJobListHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerAID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("standalone-list-a-%d@example.com", uniqueSuffix))
	ownerBID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("standalone-list-b-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = ANY($1::bigint[])", []int64{ownerAID, ownerBID})
	})
	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerACookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerAID)
	ownerBCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerBID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	answerService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	ownerA, _ := accessdomain.NewOwnerScope(ownerAID)
	ownerB, _ := accessdomain.NewOwnerScope(ownerBID)
	for index := range 2 {
		_, err := answerRepository.CreateAnswerJob(ctx, ownerA, answerapplication.Input{
			Query: fmt.Sprintf("standalone list %d", index), TopK: 5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		})
		if err != nil {
			t.Fatalf("create owner A standalone %d: %v", index, err)
		}
	}
	standaloneB, err := answerRepository.CreateAnswerJob(ctx, ownerB, answerapplication.Input{
		Query: "owner B standalone", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("create owner B standalone: %v", err)
	}
	conversation, err := conversationRepository.CreateConversation(ctx, ownerA, conversationdomain.CreateInput{Title: "hidden from standalone list"})
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, ownerA, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "linked question",
	})
	if err != nil {
		t.Fatalf("start linked turn: %v", err)
	}
	linked, err := answerService.QueueConversation(ctx, ownerA, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID, UserMessageID: turn.UserMessage.ID, AssistantMessageID: turn.AssistantMessage.ID,
		Input: answerapplication.Input{Query: "linked question", TopK: 5, ResponseLanguage: answerapplication.ResponseLanguageEnglish},
	})
	if err != nil {
		t.Fatalf("create linked job: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewAnswerJobHandler(answerService, logger).RegisterRoutes(protectedRoutes)
	api.NewConversationAnswerHandler(conversationService, answerService, logger).RegisterRoutes(protectedRoutes)

	type listPayload struct {
		Jobs []struct {
			ID             int64  `json:"id"`
			ConversationID *int64 `json:"conversation_id"`
		} `json:"jobs"`
		Pagination struct {
			Total int64 `json:"total"`
		} `json:"pagination"`
	}
	readList := func(cookie string) listPayload {
		t.Helper()
		response := performDocumentOwnerRequest(router, http.MethodGet, "/answer-jobs?page=1&page_size=20", nil, cookie, "")
		if response.Code != http.StatusOK {
			t.Fatalf("standalone list status/body = %d / %s", response.Code, response.Body.String())
		}
		var payload listPayload
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode standalone list: %v", err)
		}
		return payload
	}
	ownerAList := readList(ownerACookie)
	if len(ownerAList.Jobs) != 2 || ownerAList.Pagination.Total != 2 {
		t.Fatalf("owner A standalone list = %+v", ownerAList)
	}
	for _, job := range ownerAList.Jobs {
		if job.ConversationID != nil || job.ID == linked.ID {
			t.Fatalf("owner A standalone list leaked linked job %+v", job)
		}
	}
	ownerBList := readList(ownerBCookie)
	if len(ownerBList.Jobs) != 1 || ownerBList.Pagination.Total != 1 || ownerBList.Jobs[0].ID != standaloneB.ID {
		t.Fatalf("owner B standalone list = %+v", ownerBList)
	}
	latestResponse := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		fmt.Sprintf("/conversations/%d/answer-jobs/latest", conversation.ID),
		nil,
		ownerACookie,
		"",
	)
	if latestResponse.Code != http.StatusOK || !strings.Contains(latestResponse.Body.String(), fmt.Sprintf(`"id":%d`, linked.ID)) {
		t.Fatalf("conversation latest status/body = %d / %s", latestResponse.Code, latestResponse.Body.String())
	}
}

func TestConversationAnswerDocumentSnapshotHTTPWithPostgreSQL(t *testing.T) {
	if os.Getenv("RUN_DATABASE_TESTS") != "1" {
		t.Skip("set RUN_DATABASE_TESTS=1 to run PostgreSQL integration tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openDocumentOwnerIntegrationPool(t, ctx)
	uniqueSuffix := time.Now().UnixNano()
	ownerID := insertDocumentOwnerIntegrationUser(t, ctx, pool, fmt.Sprintf("document-snapshot-%d@example.com", uniqueSuffix))
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = pool.Exec(cleanupContext, "DELETE FROM users WHERE id = $1", ownerID)
	})
	sessionRepository := postgresrepository.NewAuthSessionRepository(pool)
	tokenManager := sessioninfrastructure.NewTokenGenerator()
	ownerCookie := createDocumentOwnerIntegrationSession(t, ctx, sessionRepository, tokenManager, ownerID)
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	sessionService, err := authapplication.NewSessionService(sessionRepository, tokenManager, time.Now)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	owner, _ := accessdomain.NewOwnerScope(ownerID)
	documentRepository := postgresrepository.NewScopedDocumentRepository(pool)
	document, err := documentRepository.Create(ctx, owner, documentdomain.CreateInput{
		OriginalName: "snapshot.pdf",
		StoragePath:  fmt.Sprintf("snapshot/%d.pdf", uniqueSuffix),
		MIMEType:     "application/pdf",
		SizeBytes:    1024,
		SHA256:       strings.Repeat("b", 64),
	})
	if err != nil {
		t.Fatalf("create snapshot document: %v", err)
	}
	conversationRepository := postgresrepository.NewConversationRepository(pool)
	conversationService := conversationapplication.NewService(conversationRepository)
	answerRepository := postgresrepository.NewAnswerJobRepository(
		pool,
		answerapplication.JobAdmissionLimits{MaxQueuedJobsPerOwner: 10, MaxQueuedJobsGlobal: 50},
		answerapplication.JobSchedulingPolicy{
			MaxInFlightPerOwner: 1, MaxBorrowedInFlightPerOwner: 1, StarvationThreshold: time.Second,
		},
	)
	answerService, err := answerapplication.NewJobService(answerRepository)
	if err != nil {
		t.Fatalf("create answer job service: %v", err)
	}
	conversation, err := conversationRepository.CreateConversation(ctx, owner, conversationdomain.CreateInput{Title: "document snapshot"})
	if err != nil {
		t.Fatalf("create snapshot conversation: %v", err)
	}
	turn, err := conversationRepository.StartTurn(ctx, owner, conversationdomain.StartTurnInput{
		ConversationID: conversation.ID, Content: "snapshot question",
	})
	if err != nil {
		t.Fatalf("start snapshot turn: %v", err)
	}
	linked, err := answerService.QueueConversation(ctx, owner, answerapplication.ConversationQueueInput{
		ConversationID: conversation.ID, UserMessageID: turn.UserMessage.ID, AssistantMessageID: turn.AssistantMessage.ID,
		Input: answerapplication.Input{
			Query: "snapshot question", DocumentID: &document.ID, TopK: 5,
			ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		},
	})
	if err != nil {
		t.Fatalf("create snapshot linked job: %v", err)
	}
	standalone, err := answerRepository.CreateAnswerJob(ctx, owner, answerapplication.Input{
		Query: "standalone snapshot", DocumentID: &document.ID, TopK: 5,
		ResponseLanguage: answerapplication.ResponseLanguageEnglish,
	})
	if err != nil {
		t.Fatalf("create snapshot standalone job: %v", err)
	}
	leaseToken := fmt.Sprintf("document-snapshot-lease-%d", uniqueSuffix)
	commandTag, err := pool.Exec(
		ctx,
		`UPDATE answer_jobs
		 SET status = 'processing', attempt_count = 1, worker_id = 'snapshot-http-test',
		     lease_token = $2, lease_expires_at = CURRENT_TIMESTAMP + INTERVAL '1 minute',
		     started_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = $1 AND status = 'queued'`,
		linked.ID,
		leaseToken,
	)
	if err != nil || commandTag.RowsAffected() != 1 {
		t.Fatalf("mark snapshot job processing = %d rows, %v", commandTag.RowsAffected(), err)
	}
	if err := documentRepository.Delete(ctx, owner, document.ID); err != nil {
		t.Fatalf("delete snapshot document: %v", err)
	}
	if _, err := answerRepository.GetAnswerJobByID(ctx, owner, standalone.ID); !errors.Is(err, answerapplication.ErrAnswerJobNotFound) {
		t.Fatalf("standalone snapshot job after document delete = %v", err)
	}
	if err := answerRepository.MarkAnswerJobSucceeded(ctx, linked.ID, leaseToken, answerapplication.Output{
		Query: "snapshot question", Answer: "snapshot answer", ResponseLanguage: answerapplication.ResponseLanguageEnglish,
		Sources:      []answerapplication.Source{{Citation: 1, ChunkID: 7, DocumentID: document.ID, OriginalName: document.OriginalName}},
		PromptTokens: 8, CompletionTokens: 3, TotalTokens: 11,
	}); err != nil {
		t.Fatalf("complete snapshot job after document delete: %v", err)
	}

	gin.SetMode(gin.TestMode)
	router := api.NewRouter(logger)
	protectedRoutes := router.Group("")
	protectedRoutes.Use(api.NewAuthMiddleware(sessionService, logger).Require)
	api.NewConversationAnswerHandler(conversationService, answerService, logger).RegisterRoutes(protectedRoutes)
	latestResponse := performDocumentOwnerRequest(
		router,
		http.MethodGet,
		fmt.Sprintf("/conversations/%d/answer-jobs/latest", conversation.ID),
		nil,
		ownerCookie,
		"",
	)
	if latestResponse.Code != http.StatusOK ||
		!strings.Contains(latestResponse.Body.String(), fmt.Sprintf(`"document_id":%d`, document.ID)) ||
		!strings.Contains(latestResponse.Body.String(), `"answer":"snapshot answer"`) ||
		!strings.Contains(latestResponse.Body.String(), `"total_tokens":11`) {
		t.Fatalf("snapshot latest status/body = %d / %s", latestResponse.Code, latestResponse.Body.String())
	}
	retryResponse := performDocumentOwnerRequest(
		router,
		http.MethodPost,
		fmt.Sprintf("/conversations/%d/answer-jobs", conversation.ID),
		strings.NewReader(fmt.Sprintf(`{"content":"retry snapshot","document_id":%d,"top_k":5,"response_language":"en"}`, document.ID)),
		ownerCookie,
		"application/json",
	)
	if retryResponse.Code != http.StatusNotFound ||
		!strings.Contains(retryResponse.Body.String(), `"code":"document_not_found"`) {
		t.Fatalf("snapshot retry status/body = %d / %s", retryResponse.Code, retryResponse.Body.String())
	}
}
