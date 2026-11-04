package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	conversationapplication "rag-reasoning-platform/backend/internal/application/conversation"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

type conversationService interface {
	Create(context.Context, accessdomain.OwnerScope, conversationapplication.CreateInput) (conversationdomain.Conversation, error)
	List(context.Context, accessdomain.OwnerScope, conversationapplication.ListInput) (conversationapplication.ListOutput, error)
	ListCursor(context.Context, accessdomain.OwnerScope, conversationapplication.ConversationCursorInput) (conversationapplication.ConversationCursorOutput, error)
	Get(context.Context, accessdomain.OwnerScope, int64) (conversationdomain.Conversation, error)
	Update(context.Context, accessdomain.OwnerScope, conversationapplication.UpdateInput) (conversationdomain.Conversation, error)
	Delete(context.Context, accessdomain.OwnerScope, int64) error
	ListMessages(context.Context, accessdomain.OwnerScope, conversationapplication.MessageListInput) (conversationapplication.MessageListOutput, error)
	ListMessagesCursor(context.Context, accessdomain.OwnerScope, conversationapplication.MessageCursorInput) (conversationapplication.MessageCursorOutput, error)
	AppendUserMessage(context.Context, accessdomain.OwnerScope, conversationapplication.AppendUserMessageInput) (conversationdomain.AppendUserMessageResult, error)
}

type ConversationHandler struct {
	service conversationService
	logger  *slog.Logger
}

func NewConversationHandler(service conversationService, logger *slog.Logger) *ConversationHandler {
	if service == nil {
		panic("NewConversationHandler requires a non-nil service")
	}
	if logger == nil {
		panic("NewConversationHandler requires a non-nil logger")
	}
	return &ConversationHandler{service: service, logger: logger}
}

func (h *ConversationHandler) RegisterRoutes(router gin.IRoutes) {
	router.POST("/conversations", h.Create)
	router.GET("/conversations", h.List)
	router.GET("/conversations/cursor", h.ListCursor)
	router.GET("/conversations/:id", h.Get)
	router.PATCH("/conversations/:id", h.Update)
	router.DELETE("/conversations/:id", h.Delete)
	router.GET("/conversations/:id/messages", h.ListMessages)
	router.GET("/conversations/:id/messages/cursor", h.ListMessagesCursor)
	router.POST("/conversations/:id/messages", h.AppendUserMessage)
}

type createConversationRequest struct {
	Title string `json:"title"`
}

type updateConversationRequest struct {
	Title  *string `json:"title"`
	Status *string `json:"status"`
}

type appendConversationMessageRequest struct {
	Content         string  `json:"content"`
	ClientMessageID *string `json:"client_message_id"`
}

type conversationResponse struct {
	ID            int64      `json:"id"`
	Title         string     `json:"title"`
	Status        string     `json:"status"`
	LastMessageAt *time.Time `json:"last_message_at"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type conversationListResponse struct {
	Conversations []conversationResponse `json:"conversations"`
	Pagination    paginationResponse     `json:"pagination"`
}

type cursorPageResponse struct {
	Next    *string `json:"next"`
	HasMore bool    `json:"has_more"`
}

type conversationCursorListResponse struct {
	Conversations []conversationResponse `json:"conversations"`
	Cursor        cursorPageResponse     `json:"cursor"`
}

type conversationMessageResponse struct {
	ID              int64     `json:"id"`
	ConversationID  int64     `json:"conversation_id"`
	SequenceNo      int64     `json:"sequence_no"`
	Role            string    `json:"role"`
	Content         string    `json:"content"`
	Status          string    `json:"status"`
	ClientMessageID *string   `json:"client_message_id"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type conversationMessageListResponse struct {
	ConversationID int64                         `json:"conversation_id"`
	Messages       []conversationMessageResponse `json:"messages"`
	Pagination     paginationResponse            `json:"pagination"`
}

type conversationMessageCursorListResponse struct {
	ConversationID int64                         `json:"conversation_id"`
	Messages       []conversationMessageResponse `json:"messages"`
	Cursor         cursorPageResponse            `json:"cursor"`
}

type appendConversationMessageResponse struct {
	Created bool                        `json:"created"`
	Message conversationMessageResponse `json:"message"`
}

func (h *ConversationHandler) Create(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}

	var request createConversationRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		// 空请求体等价于不提供标题，应用层会使用默认标题。
		// 其他 JSON 解析错误仍然必须拒绝，避免把坏请求静默当成默认会话。
		if errors.Is(err, io.EOF) {
			request = createConversationRequest{}
		} else {
			writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "request body must be valid JSON")
			return
		}
	}
	created, err := h.service.Create(c.Request.Context(), scope, conversationapplication.CreateInput{Title: request.Title})
	if errors.Is(err, conversationapplication.ErrInvalidTitle) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation title must be between 1 and 200 characters")
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "conversation_create_failed", err)
		return
	}
	c.JSON(http.StatusCreated, newConversationResponse(created))
}

func (h *ConversationHandler) List(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	page, pageSize, ok := parseConversationPagination(c)
	if !ok {
		return
	}

	result, err := h.service.List(c.Request.Context(), scope, conversationapplication.ListInput{Page: page, PageSize: pageSize})
	if errors.Is(err, conversationapplication.ErrInvalidPage) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPage, "page must be a positive integer")
		return
	}
	if errors.Is(err, conversationapplication.ErrInvalidPageSize) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be between 1 and 100")
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "conversation_list_failed", err)
		return
	}

	conversations := make([]conversationResponse, 0, len(result.Conversations))
	for _, conversation := range result.Conversations {
		conversations = append(conversations, newConversationResponse(conversation))
	}
	c.JSON(http.StatusOK, conversationListResponse{
		Conversations: conversations,
		Pagination: paginationResponse{
			Page: result.Page, PageSize: result.PageSize, Total: result.Total, TotalPages: result.TotalPages,
		},
	})
}

func (h *ConversationHandler) ListCursor(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	pageSize, err := parsePositiveQueryInt(c, "page_size", conversationapplication.DefaultConversationCursorPageSize)
	if err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be a positive integer")
		return
	}
	cursor, ok := parseOpaqueConversationCursor(c, errorCodeInvalidConversationCursor)
	if !ok {
		return
	}
	result, err := h.service.ListCursor(c.Request.Context(), scope, conversationapplication.ConversationCursorInput{
		PageSize: pageSize, Cursor: cursor,
	})
	switch {
	case err == nil:
		conversations := make([]conversationResponse, 0, len(result.Conversations))
		for _, conversation := range result.Conversations {
			conversations = append(conversations, newConversationResponse(conversation))
		}
		c.JSON(http.StatusOK, conversationCursorListResponse{
			Conversations: conversations,
			Cursor:        cursorPageResponse{Next: result.Cursor.Next, HasMore: result.Cursor.HasMore},
		})
	case errors.Is(err, conversationapplication.ErrInvalidPageSize):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be between 1 and 100")
	case errors.Is(err, conversationapplication.ErrInvalidConversationCursor):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationCursor, "conversation cursor is invalid")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_cursor_list_failed", err)
	}
}

func (h *ConversationHandler) Get(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	found, err := h.service.Get(c.Request.Context(), scope, conversationID)
	if errors.Is(err, conversationdomain.ErrNotFound) {
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "conversation_get_failed", err, slog.Int64("conversation_id", conversationID))
		return
	}
	c.JSON(http.StatusOK, newConversationResponse(found))
}

func (h *ConversationHandler) Update(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	var request updateConversationRequest
	if err := decodeStrictConversationUpdate(c, &request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "request body must be a valid conversation update")
		return
	}
	var status *conversationdomain.Status
	if request.Status != nil {
		value := conversationdomain.Status(*request.Status)
		status = &value
	}
	updated, err := h.service.Update(c.Request.Context(), scope, conversationapplication.UpdateInput{
		ConversationID: conversationID,
		Title:          request.Title,
		Status:         status,
	})
	switch {
	case err == nil:
		c.JSON(http.StatusOK, newConversationResponse(updated))
	case errors.Is(err, conversationapplication.ErrInvalidID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	case errors.Is(err, conversationapplication.ErrEmptyUpdate):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "title or status is required")
	case errors.Is(err, conversationapplication.ErrInvalidTitle):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation title must be between 1 and 200 characters")
	case errors.Is(err, conversationapplication.ErrInvalidStatus):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation status must be active or archived")
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, conversationdomain.ErrAnswerActive):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationAnswerActive, "conversation has an active answer")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_update_failed", err, slog.Int64("conversation_id", conversationID))
	}
}

func (h *ConversationHandler) Delete(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	err := h.service.Delete(c.Request.Context(), scope, conversationID)
	switch {
	case err == nil:
		c.Status(http.StatusNoContent)
	case errors.Is(err, conversationapplication.ErrInvalidID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, conversationdomain.ErrAnswerActive):
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationAnswerActive, "conversation has an active answer")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_delete_failed", err, slog.Int64("conversation_id", conversationID))
	}
}

func (h *ConversationHandler) ListMessages(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	page, pageSize, ok := parseConversationPagination(c)
	if !ok {
		return
	}
	order, ok := parseConversationMessageOrder(c)
	if !ok {
		return
	}

	result, err := h.service.ListMessages(c.Request.Context(), scope, conversationapplication.MessageListInput{
		ConversationID: conversationID, Page: page, PageSize: pageSize, Order: order,
	})
	if errors.Is(err, conversationdomain.ErrNotFound) {
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
		return
	}
	if errors.Is(err, conversationapplication.ErrInvalidPage) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPage, "page must be a positive integer")
		return
	}
	if errors.Is(err, conversationapplication.ErrInvalidPageSize) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be between 1 and 100")
		return
	}
	if errors.Is(err, conversationapplication.ErrInvalidMessageOrder) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationMessageOrder, "order must be asc or desc")
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "conversation_messages_list_failed", err, slog.Int64("conversation_id", conversationID))
		return
	}

	messages := make([]conversationMessageResponse, 0, len(result.Messages))
	for _, message := range result.Messages {
		messages = append(messages, newConversationMessageResponse(message))
	}
	c.JSON(http.StatusOK, conversationMessageListResponse{
		ConversationID: result.ConversationID,
		Messages:       messages,
		Pagination: paginationResponse{
			Page: result.Page, PageSize: result.PageSize, Total: result.Total, TotalPages: result.TotalPages,
		},
	})
}

func (h *ConversationHandler) ListMessagesCursor(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	pageSize, err := parsePositiveQueryInt(c, "page_size", conversationapplication.DefaultMessageCursorPageSize)
	if err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be a positive integer")
		return
	}
	cursor, ok := parseOpaqueConversationCursor(c, errorCodeInvalidConversationMessageCursor)
	if !ok {
		return
	}
	result, err := h.service.ListMessagesCursor(c.Request.Context(), scope, conversationapplication.MessageCursorInput{
		ConversationID: conversationID, PageSize: pageSize, Cursor: cursor,
	})
	switch {
	case err == nil:
		messages := make([]conversationMessageResponse, 0, len(result.Messages))
		for _, message := range result.Messages {
			messages = append(messages, newConversationMessageResponse(message))
		}
		c.JSON(http.StatusOK, conversationMessageCursorListResponse{
			ConversationID: result.ConversationID,
			Messages:       messages,
			Cursor:         cursorPageResponse{Next: result.Cursor.Next, HasMore: result.Cursor.HasMore},
		})
	case errors.Is(err, conversationdomain.ErrNotFound):
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
	case errors.Is(err, conversationapplication.ErrInvalidID):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
	case errors.Is(err, conversationapplication.ErrInvalidPageSize):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be between 1 and 100")
	case errors.Is(err, conversationapplication.ErrInvalidMessageCursor):
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationMessageCursor, "conversation message cursor is invalid")
	default:
		writeInternalErrorResponse(c, h.logger, "conversation_message_cursor_list_failed", err, slog.Int64("conversation_id", conversationID))
	}
}

func (h *ConversationHandler) AppendUserMessage(c *gin.Context) {
	scope, authenticated := ownerScopeFromContext(c)
	if !authenticated {
		writeAuthenticationRequired(c)
		return
	}
	conversationID, ok := parseConversationID(c)
	if !ok {
		return
	}
	var request appendConversationMessageRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "request body must be valid JSON")
		return
	}
	result, err := h.service.AppendUserMessage(c.Request.Context(), scope, conversationapplication.AppendUserMessageInput{
		ConversationID:  conversationID,
		Content:         request.Content,
		ClientMessageID: request.ClientMessageID,
	})
	if errors.Is(err, conversationapplication.ErrInvalidID) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
		return
	}
	if errors.Is(err, conversationapplication.ErrInvalidMessage) || errors.Is(err, conversationapplication.ErrInvalidClientMessageID) {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationRequest, "conversation message is invalid")
		return
	}
	if errors.Is(err, conversationdomain.ErrNotFound) {
		writeErrorResponse(c, http.StatusNotFound, errorCodeConversationNotFound, "conversation not found")
		return
	}
	if errors.Is(err, conversationdomain.ErrDuplicateClientMessage) {
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationDuplicateMessage, "client message ID was already used with different content")
		return
	}
	if errors.Is(err, conversationdomain.ErrArchived) {
		writeErrorResponse(c, http.StatusConflict, errorCodeConversationArchived, "conversation is archived")
		return
	}
	if err != nil {
		writeInternalErrorResponse(c, h.logger, "conversation_message_append_failed", err, slog.Int64("conversation_id", conversationID))
		return
	}
	status := http.StatusOK
	if result.Created {
		status = http.StatusCreated
	}
	c.JSON(status, appendConversationMessageResponse{
		Created: result.Created,
		Message: newConversationMessageResponse(result.Message),
	})
}

func parseConversationID(c *gin.Context) (int64, bool) {
	conversationID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || conversationID <= 0 {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationID, "conversation ID must be a positive integer")
		return 0, false
	}
	return conversationID, true
}

func decodeStrictConversationUpdate(c *gin.Context, request *updateConversationRequest) error {
	decoder := json.NewDecoder(c.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(request); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("conversation update body must contain one JSON value")
		}
		return err
	}
	return nil
}

func parseConversationPagination(c *gin.Context) (int64, int64, bool) {
	page, err := parsePositiveQueryInt(c, "page", 1)
	if err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPage, "page must be a positive integer")
		return 0, 0, false
	}
	pageSize, err := parsePositiveQueryInt(c, "page_size", conversationapplication.DefaultPageSize)
	if err != nil {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationPageSize, "page_size must be a positive integer")
		return 0, 0, false
	}
	return page, pageSize, true
}

func parseConversationMessageOrder(c *gin.Context) (conversationdomain.MessageOrder, bool) {
	values, provided := c.Request.URL.Query()["order"]
	if !provided {
		return conversationdomain.MessageOrderAscending, true
	}
	if len(values) != 1 {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationMessageOrder, "order must be asc or desc")
		return "", false
	}
	order := conversationdomain.MessageOrder(values[0])
	if !order.IsValid() {
		writeErrorResponse(c, http.StatusBadRequest, errorCodeInvalidConversationMessageOrder, "order must be asc or desc")
		return "", false
	}
	return order, true
}

func parseOpaqueConversationCursor(c *gin.Context, errorCode string) (string, bool) {
	values, provided := c.Request.URL.Query()["cursor"]
	if !provided {
		return "", true
	}
	if len(values) != 1 || values[0] == "" {
		writeErrorResponse(c, http.StatusBadRequest, errorCode, "cursor is invalid")
		return "", false
	}
	return values[0], true
}

func newConversationResponse(source conversationdomain.Conversation) conversationResponse {
	return conversationResponse{
		ID: source.ID, Title: source.Title, Status: string(source.Status),
		LastMessageAt: source.LastMessageAt, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}

func newConversationMessageResponse(source conversationdomain.Message) conversationMessageResponse {
	return conversationMessageResponse{
		ID: source.ID, ConversationID: source.ConversationID, SequenceNo: source.SequenceNo,
		Role: string(source.Role), Content: source.Content, Status: string(source.Status),
		ClientMessageID: source.ClientMessageID, CreatedAt: source.CreatedAt, UpdatedAt: source.UpdatedAt,
	}
}
