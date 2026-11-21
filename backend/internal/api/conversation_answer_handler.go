package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	conversationapplication "rag-reasoning-platform/backend/internal/application/conversation"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const conversationQueueCleanupTimeout = 5 * time.Second

type conversationTurnService interface {
	StartTurn(
		context.Context,
		accessdomain.OwnerScope,
		conversationapplication.StartTurnInput,
	) (conversationdomain.StartTurnResult, error)
	FailAssistantIfUnqueued(
		context.Context,
		accessdomain.OwnerScope,
		conversationapplication.FailAssistantIfUnqueuedInput,
	) (conversationdomain.Message, error)
}

type conversationAnswerJobService interface {
	QueueConversation(
		context.Context,
		accessdomain.OwnerScope,
		answerapplication.ConversationQueueInput,
	) (answerapplication.Job, error)
	GetActiveConversationJob(
		context.Context,
		accessdomain.OwnerScope,
		int64,
	) (answerapplication.Job, error)
	GetLatestConversationJob(
		context.Context,
		accessdomain.OwnerScope,
		int64,
	) (answerapplication.Job, error)
}

type ConversationAnswerHandler struct {
	turns  conversationTurnService
	answer conversationAnswerJobService
	logger *slog.Logger
}

func NewConversationAnswerHandler(
	turns conversationTurnService,
	answer conversationAnswerJobService,
	logger *slog.Logger,
) *ConversationAnswerHandler {
	if turns == nil || answer == nil {
		panic("NewConversationAnswerHandler requires non-nil services")
	}
	if logger == nil {
		panic("NewConversationAnswerHandler requires a non-nil logger")
	}
	return &ConversationAnswerHandler{turns: turns, answer: answer, logger: logger}
}

func (h *ConversationAnswerHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/conversations/:id/answer-jobs", h.Queue)
	router.GET("/conversations/:id/answer-jobs/active", h.GetActive)
	router.GET("/conversations/:id/answer-jobs/latest", h.GetLatest)
}

type conversationAnswerRequest struct {
	Content          string  `json:"content"`
	ClientMessageID  *string `json:"client_message_id"`
	DocumentID       *int64  `json:"document_id"`
	TopK             *int    `json:"top_k"`
	ResponseLanguage string  `json:"response_language"`
}

type conversationAnswerResponse struct {
	ConversationID int64                       `json:"conversation_id"`
	UserMessage    conversationMessageResponse `json:"user_message"`
	Assistant      conversationMessageResponse `json:"assistant_message"`
	AnswerJob      answerJobResponse           `json:"answer_job"`
}

func (h *ConversationAnswerHandler) Queue(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}

	var request conversationAnswerRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "request body must be valid JSON")
		return
	}
	topK := defaultAnswerTopK
	if request.TopK != nil {
		topK = *request.TopK
	}
	validatedQuery, validationErr := embeddingapplication.ValidateSemanticSearchInput(
		embeddingapplication.SemanticSearchInput{
			Query: request.Content, DocumentID: request.DocumentID, TopK: topK,
		},
	)
	if h.writeQueueError(c, conversationID, validationErr) {
		return
	}
	request.Content = validatedQuery
	if _, validationErr := answerapplication.NormalizeResponseLanguagePreference(
		answerapplication.ResponseLanguage(request.ResponseLanguage),
	); h.writeQueueError(c, conversationID, validationErr) {
		return
	}

	turn, err := h.turns.StartTurn(
		c.Request.Context(),
		scope,
		conversationapplication.StartTurnInput{
			ConversationID:  conversationID,
			Content:         request.Content,
			ClientMessageID: request.ClientMessageID,
		},
	)
	if h.writeTurnError(c, conversationID, err) {
		return
	}
	if validationErr := validateConversationTurnResult(conversationID, turn); validationErr != nil {
		writeInternalErrorResponse(
			c,
			h.logger,
			"conversation_turn_result_invalid",
			validationErr,
			slog.Int64("conversation_id", conversationID),
		)
		return
	}

	job, err := h.answer.QueueConversation(
		c.Request.Context(),
		scope,
		answerapplication.ConversationQueueInput{
			ConversationID:     conversationID,
			UserMessageID:      turn.UserMessage.ID,
			AssistantMessageID: turn.AssistantMessage.ID,
			Input: answerapplication.Input{
				Query:            request.Content,
				DocumentID:       request.DocumentID,
				TopK:             topK,
				ResponseLanguage: answerapplication.ResponseLanguage(request.ResponseLanguage),
			},
		},
	)
	if h.writeQueueError(c, conversationID, err) {
		cleanupContext, cancelCleanup := context.WithTimeout(
			context.WithoutCancel(c.Request.Context()),
			conversationQueueCleanupTimeout,
		)
		defer cancelCleanup()
		if _, cleanupErr := h.turns.FailAssistantIfUnqueued(
			cleanupContext,
			scope,
			conversationapplication.FailAssistantIfUnqueuedInput{
				ConversationID: conversationID,
				MessageID:      turn.AssistantMessage.ID,
				Content:        "回答任务未能进入队列，请稍后重试。",
			},
		); cleanupErr != nil {
			h.logger.Error("failed to close assistant placeholder after queue error", "event", "conversation_assistant_placeholder_cleanup_failed", "error", cleanupErr)
		}
		return
	}
	if validationErr := validateConversationAnswerJob(conversationID, turn, job); validationErr != nil {
		writeInternalErrorResponse(
			c,
			h.logger,
			"conversation_answer_job_result_invalid",
			validationErr,
			slog.Int64("conversation_id", conversationID),
			slog.Int64("answer_job_id", job.ID),
		)
		return
	}

	status := http.StatusAccepted
	if !turn.Created {
		status = http.StatusOK
	}
	assistant := turn.AssistantMessage
	if job.Status == answerapplication.JobStatusQueued || job.Status == answerapplication.JobStatusProcessing {
		assistant.Content = conversationdomain.AssistantGeneratingPlaceholder
		assistant.Status = conversationdomain.MessageStatusGenerating
	}
	c.JSON(status, conversationAnswerResponse{
		ConversationID: conversationID,
		UserMessage:    newConversationMessageResponse(turn.UserMessage),
		Assistant:      newConversationMessageResponse(assistant),
		AnswerJob:      newAnswerJobResponse(job),
	})
}

func validateConversationTurnResult(
	conversationID int64,
	turn conversationdomain.StartTurnResult,
) error {
	if turn.UserMessage.ID <= 0 || turn.AssistantMessage.ID <= 0 {
		return fmt.Errorf(
			"conversation turn returned invalid message IDs user=%d assistant=%d",
			turn.UserMessage.ID,
			turn.AssistantMessage.ID,
		)
	}
	if turn.UserMessage.ConversationID != conversationID ||
		turn.AssistantMessage.ConversationID != conversationID {
		return fmt.Errorf(
			"conversation turn association mismatch request=%d user=%d assistant=%d",
			conversationID,
			turn.UserMessage.ConversationID,
			turn.AssistantMessage.ConversationID,
		)
	}
	if turn.UserMessage.Role != conversationdomain.RoleUser ||
		turn.AssistantMessage.Role != conversationdomain.RoleAssistant {
		return fmt.Errorf(
			"conversation turn role mismatch user=%q assistant=%q",
			turn.UserMessage.Role,
			turn.AssistantMessage.Role,
		)
	}
	return nil
}

func validateConversationAnswerJob(
	conversationID int64,
	turn conversationdomain.StartTurnResult,
	job answerapplication.Job,
) error {
	if job.ID <= 0 || job.ConversationID == nil || job.UserMessageID == nil || job.AssistantMessageID == nil {
		return fmt.Errorf("conversation answer job %d is missing required associations", job.ID)
	}
	if *job.ConversationID != conversationID ||
		*job.UserMessageID != turn.UserMessage.ID ||
		*job.AssistantMessageID != turn.AssistantMessage.ID {
		return fmt.Errorf(
			"conversation answer job association mismatch request=%d job_conversation=%d user=%d/%d assistant=%d/%d",
			conversationID,
			*job.ConversationID,
			*job.UserMessageID,
			turn.UserMessage.ID,
			*job.AssistantMessageID,
			turn.AssistantMessage.ID,
		)
	}
	return nil
}

// GetActive 返回当前用户会话唯一的 queued/processing 回答任务。
func (h *ConversationAnswerHandler) GetActive(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}

	job, err := h.answer.GetActiveConversationJob(c.Request.Context(), scope, conversationID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, newAnswerJobResponse(job))
	case errors.Is(err, answerapplication.ErrAnswerJobNotFound):
		c.Status(http.StatusNoContent)
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, answerapplication.ErrInvalidAnswerJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_active_answer_lookup_failed", err, slog.Int64("conversation_id", conversationID))
	}
}

// GetLatest 返回当前用户会话保留期内最新的回答任务。
func (h *ConversationAnswerHandler) GetLatest(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}

	job, err := h.answer.GetLatestConversationJob(c.Request.Context(), scope, conversationID)
	switch {
	case err == nil:
		c.JSON(http.StatusOK, newAnswerJobResponse(job))
	case errors.Is(err, answerapplication.ErrAnswerJobNotFound):
		c.Status(http.StatusNoContent)
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, answerapplication.ErrInvalidAnswerJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_latest_answer_lookup_failed", err, slog.Int64("conversation_id", conversationID))
	}
}

func (h *ConversationAnswerHandler) writeTurnError(c *gin.Context, conversationID int64, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, conversationapplication.ErrInvalidID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	case errors.Is(err, conversationapplication.ErrInvalidMessage), errors.Is(err, conversationapplication.ErrInvalidClientMessageID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation message is invalid")
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, conversationdomain.ErrDuplicateClientMessage):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationDuplicateMessage, "client message ID was already used with different content")
	case errors.Is(err, conversationdomain.ErrArchived):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationArchived, "conversation is archived")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_turn_start_failed", err, slog.Int64("conversation_id", conversationID))
	}
	return true
}

func (h *ConversationAnswerHandler) writeQueueError(c *gin.Context, conversationID int64, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, answerapplication.ErrInvalidAnswerJobID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation answer request is invalid")
	case errors.Is(err, answerapplication.ErrConversationAnswerActive):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationAnswerActive, "this conversation already has an answer being generated")
	case errors.Is(err, embeddingapplication.ErrInvalidDocumentID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "document_id must be a positive integer")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryRequired):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "content is required")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryInvalidUTF8):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "content must be valid UTF-8")
	case errors.Is(err, embeddingapplication.ErrSemanticSearchQueryTooLong):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "content must not exceed 1000 characters for answer generation")
	case errors.Is(err, embeddingapplication.ErrInvalidSemanticSearchTopK), errors.Is(err, answerapplication.ErrInvalidResponseLanguage):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "answer parameters are invalid")
	case errors.Is(err, conversationdomain.ErrArchived):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationArchived, "conversation is archived")
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, documentdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeDocumentNotFound, "document not found")
	case errors.Is(err, answerapplication.ErrAnswerOwnerQueueCapacity):
		c.Header("Retry-After", "5")
		writeErrorResponse(c, http.StatusTooManyRequests, errorCodeAnswerJobOwnerQueueCapacity, "too many queued answer jobs for this user")
	case errors.Is(err, answerapplication.ErrAnswerGlobalQueueCapacity):
		c.Header("Retry-After", "5")
		writeErrorResponse(c, http.StatusServiceUnavailable, errorCodeAnswerJobGlobalQueueCapacity, "answer job queue is temporarily full")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_answer_queue_failed", err, slog.Int64("conversation_id", conversationID))
	}
	return true
}
