package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

const (
	maxCreateAgentRunBodyBytes = agentapplication.MaxContentBytes*6 + 4096
	agentCapacityRetryAfter    = "5"
)

type agentRunService interface {
	Queue(context.Context, accessdomain.OwnerScope, agentapplication.QueueInput) (agentapplication.QueueResult, error)
	GetByID(context.Context, accessdomain.OwnerScope, int64) (agentapplication.RunSnapshot, error)
	GetActiveConversationRun(context.Context, accessdomain.OwnerScope, int64) (agentapplication.RunSnapshot, error)
	GetLatestConversationRun(context.Context, accessdomain.OwnerScope, int64) (agentapplication.RunSnapshot, error)
	Cancel(context.Context, accessdomain.OwnerScope, int64) (agentapplication.RunSnapshot, error)
}

// AgentRunHandler exposes the frozen Agent v0 polling API. Conditional route
// registration remains a B2-C assembly responsibility.
type AgentRunHandler struct {
	service agentRunService
	logger  *slog.Logger
}

func NewAgentRunHandler(service agentRunService, logger *slog.Logger) *AgentRunHandler {
	if service == nil {
		panic("NewAgentRunHandler requires a non-nil service")
	}
	if logger == nil {
		panic("NewAgentRunHandler requires a non-nil logger")
	}
	return &AgentRunHandler{service: service, logger: logger}
}

func (h *AgentRunHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/conversations/:id/agent-runs", h.Queue)
	router.GET("/agent-runs/:id", h.GetByID)
	router.GET("/conversations/:id/agent-runs/active", h.GetActive)
	router.GET("/conversations/:id/agent-runs/latest", h.GetLatest)
	router.POST("/agent-runs/:id/cancel", h.Cancel)
}

type createAgentRunRequest struct {
	Content          string  `json:"content"`
	ClientRunID      string  `json:"client_run_id"`
	DocumentID       *int64  `json:"document_id"`
	ResponseLanguage *string `json:"response_language"`
}

func (h *AgentRunHandler) Queue(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	var request createAgentRunRequest
	if err := decodeCreateAgentRunRequest(c, &request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAgentRunRequest, "agent run request must be one valid JSON object")
		return
	}
	language := agentdomain.ResponseLanguage("")
	if request.ResponseLanguage != nil {
		language = agentdomain.ResponseLanguage(*request.ResponseLanguage)
	}
	result, err := h.service.Queue(c.Request.Context(), scope, agentapplication.QueueInput{
		ConversationID: conversationID, Content: request.Content, ClientRunID: request.ClientRunID,
		RequestedDocumentID: request.DocumentID, ResponseLanguage: language,
	})
	if h.writeQueueError(c, conversationID, err) {
		return
	}
	response, err := newCreateAgentRunResponse(conversationID, scope.OwnerUserID(), request, result)
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "agent_run_create_snapshot_invalid", err, slog.Int64("conversation_id", conversationID))
		return
	}
	status := http.StatusAccepted
	if !result.Created {
		status = http.StatusOK
	}
	c.JSON(status, response)
}

func (h *AgentRunHandler) GetByID(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	runID, ok := parseAgentRunID(c)
	if !ok {
		return
	}
	snapshot, err := h.service.GetByID(c.Request.Context(), scope, runID)
	if h.writeRunLookupError(c, runID, err, "agent_run_get_failed") {
		return
	}
	if snapshot.ID != runID {
		writeInternalErrorResponse(c, h.logger, "agent_run_get_snapshot_invalid", errors.New("agent Run lookup returned the wrong Run"), slog.Int64("agent_run_id", runID))
		return
	}
	h.writeSnapshot(c, scope, snapshot, "agent_run_get_snapshot_invalid", slog.Int64("agent_run_id", runID))
}

func (h *AgentRunHandler) GetActive(c *gin.Context) {
	h.getConversationSnapshot(c, true)
}

func (h *AgentRunHandler) GetLatest(c *gin.Context) {
	h.getConversationSnapshot(c, false)
}

func (h *AgentRunHandler) getConversationSnapshot(c *gin.Context, active bool) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	var snapshot agentapplication.RunSnapshot
	var err error
	diagnostic := "agent_run_latest_lookup_failed"
	if active {
		snapshot, err = h.service.GetActiveConversationRun(c.Request.Context(), scope, conversationID)
		diagnostic = "agent_run_active_lookup_failed"
	} else {
		snapshot, err = h.service.GetLatestConversationRun(c.Request.Context(), scope, conversationID)
	}
	switch {
	case err == nil:
		if snapshot.ConversationID != conversationID {
			writeInternalErrorResponse(c, h.logger, diagnostic+"_snapshot_invalid", errors.New("conversation lookup returned an agent Run from another conversation"), slog.Int64("conversation_id", conversationID))
			return
		}
		if active && !snapshot.Status.IsActive() {
			writeInternalErrorResponse(c, h.logger, diagnostic+"_snapshot_invalid", errors.New("active lookup returned a non-active agent Run"), slog.Int64("conversation_id", conversationID))
			return
		}
		mapped, mapErr := newAgentRunResponse(scope.OwnerUserID(), snapshot)
		if mapErr != nil {
			writeInternalErrorResponse(c, h.logger, diagnostic+"_snapshot_invalid", mapErr, slog.Int64("conversation_id", conversationID))
			return
		}
		c.JSON(http.StatusOK, agentRunEnvelopeResponse{AgentRun: mapped})
	case errors.Is(err, agentapplication.ErrRunNotFound):
		c.Status(http.StatusNoContent)
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, agentapplication.ErrInvalidConversationID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	default:
		writeInternalErrorResponse(c, h.logger, diagnostic, err, slog.Int64("conversation_id", conversationID))
	}
}

func (h *AgentRunHandler) Cancel(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	runID, ok := parseAgentRunID(c)
	if !ok {
		return
	}
	snapshot, err := h.service.Cancel(c.Request.Context(), scope, runID)
	switch {
	case errors.Is(err, agentapplication.ErrInvalidRunID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAgentRunID, "agent run ID must be a positive integer")
		return
	case errors.Is(err, agentapplication.ErrRunNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeAgentRunNotFound, "agent run not found")
		return
	case errors.Is(err, agentapplication.ErrRunProcessingCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeAgentRunProcessing, "processing agent run cannot be canceled")
		return
	case errors.Is(err, agentapplication.ErrRunTerminalCannotCancel):
		writeErrorResponse(c, http.StatusConflict, errorCodeAgentRunTerminal, "terminal agent run cannot be canceled")
		return
	case err != nil:
		writeInternalErrorResponse(c, h.logger, "agent_run_cancel_failed", err, slog.Int64("agent_run_id", runID))
		return
	}
	if snapshot.ID != runID || snapshot.Status != agentdomain.RunStatusCanceled {
		writeInternalErrorResponse(c, h.logger, "agent_run_cancel_snapshot_invalid", errors.New("cancel returned a non-canceled agent Run"), slog.Int64("agent_run_id", runID))
		return
	}
	h.writeSnapshot(c, scope, snapshot, "agent_run_cancel_snapshot_invalid", slog.Int64("agent_run_id", runID))
}

func (h *AgentRunHandler) writeSnapshot(
	c *gin.Context,
	scope accessdomain.OwnerScope,
	snapshot agentapplication.RunSnapshot,
	diagnostic string,
	attributes ...slog.Attr,
) {
	mapped, err := newAgentRunResponse(scope.OwnerUserID(), snapshot)
	if err != nil {
		writeInternalErrorResponse(c, h.logger, diagnostic, err, attributes...)
		return
	}
	c.JSON(http.StatusOK, agentRunEnvelopeResponse{AgentRun: mapped})
}

func parseAgentRunID(c *gin.Context) (int64, bool) {
	runID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || runID <= 0 {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAgentRunID, "agent run ID must be a positive integer")
		return 0, false
	}
	return runID, true
}

func decodeCreateAgentRunRequest(c *gin.Context, request *createAgentRunRequest) error {
	reader := http.MaxBytesReader(c.Writer, c.Request.Body, maxCreateAgentRunBodyBytes)
	raw, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	return decodeStrictAgentJSONObject(raw, request)
}

func (h *AgentRunHandler) writeQueueError(c *gin.Context, conversationID int64, err error) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, agentapplication.ErrInvalidConversationID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	case errors.Is(err, agentapplication.ErrInvalidContent),
		errors.Is(err, agentapplication.ErrInvalidClientRunID),
		errors.Is(err, agentapplication.ErrInvalidDocumentID),
		errors.Is(err, agentapplication.ErrInvalidResponseLanguage):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAgentRunRequest, "agent run request is invalid")
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, documentdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeDocumentNotFound, "document not found")
	case errors.Is(err, conversationdomain.ErrArchived):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationArchived, "conversation is archived")
	case errors.Is(err, agentapplication.ErrIdempotencyConflict):
		writeErrorResponse(c, http.StatusConflict, errorCodeAgentRunIdempotencyConflict, "client run ID was already used with different input")
	case errors.Is(err, agentapplication.ErrConversationExecutionActive):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationExecutionActive, "conversation already has an active execution")
	case errors.Is(err, agentapplication.ErrOwnerQueueCapacity):
		c.Header("Retry-After", agentCapacityRetryAfter)
		writeErrorResponse(c, http.StatusTooManyRequests, errorCodeAgentRunOwnerQueueCapacity, "too many queued agent runs for this user")
	case errors.Is(err, agentapplication.ErrGlobalQueueCapacity):
		c.Header("Retry-After", agentCapacityRetryAfter)
		writeErrorResponse(c, http.StatusServiceUnavailable, errorCodeAgentRunQueueCapacity, "agent run queue is temporarily full")
	default:
		writeInternalErrorResponse(c, h.logger, "agent_run_queue_failed", err, slog.Int64("conversation_id", conversationID))
	}
	return true
}

func (h *AgentRunHandler) writeRunLookupError(c *gin.Context, runID int64, err error, diagnostic string) bool {
	if err == nil {
		return false
	}
	switch {
	case errors.Is(err, agentapplication.ErrInvalidRunID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidAgentRunID, "agent run ID must be a positive integer")
	case errors.Is(err, agentapplication.ErrRunNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeAgentRunNotFound, "agent run not found")
	default:
		writeInternalErrorResponse(c, h.logger, diagnostic, err, slog.Int64("agent_run_id", runID))
	}
	return true
}
