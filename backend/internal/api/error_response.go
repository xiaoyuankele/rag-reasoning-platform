package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"rag-reasoning-platform/backend/internal/observability"
)

const (
	errorCodeInvalidDocumentID              = "invalid_document_id"
	errorCodeDocumentNotFound               = "document_not_found"
	errorCodeInvalidDocumentPreflight       = "invalid_document_preflight"
	errorCodeFileTooLarge                   = "file_too_large"
	errorCodeUploadOwnerCapacity            = "upload_owner_concurrency_exhausted"
	errorCodeUploadGlobalCapacity           = "upload_capacity_exhausted"
	errorCodeInvalidEmbeddingJobID          = "invalid_embedding_job_id"
	errorCodeEmbeddingJobNotFound           = "embedding_job_not_found"
	errorCodeEmbeddingJobProcessing         = "embedding_job_processing"
	errorCodeEmbeddingJobTerminal           = "embedding_job_terminal"
	errorCodeInvalidEmbeddingBatch          = "invalid_embedding_batch"
	errorCodeInvalidEmbeddingJobLookup      = "invalid_embedding_job_lookup"
	errorCodeEmbeddingOwnerJobLimit         = "embedding_owner_active_job_limit"
	errorCodeEmbeddingQueueCapacity         = "embedding_queue_capacity_exhausted"
	errorCodeEmbeddingProviderCapacity      = "embedding_provider_capacity_exhausted"
	errorCodeInvalidProcessingJobID         = "invalid_processing_job_id"
	errorCodeProcessingJobNotFound          = "processing_job_not_found"
	errorCodeProcessingJobProcessing        = "processing_job_processing"
	errorCodeProcessingJobTerminal          = "processing_job_terminal"
	errorCodeInvalidProcessingJobLookup     = "invalid_processing_job_lookup"
	errorCodeProcessingOwnerJobLimit        = "processing_owner_active_job_limit"
	errorCodeProcessingQueueCapacity        = "processing_queue_capacity_exhausted"
	errorCodeInvalidVerificationRequest     = "invalid_verification_request"
	errorCodeVerificationRequestThrottled   = "verification_request_throttled"
	errorCodeVerificationChannelUnavailable = "verification_channel_unavailable"
	errorCodeInvalidAuthRequest             = "invalid_auth_request"
	errorCodeAuthRequestThrottled           = "auth_request_throttled"
	errorCodeVerificationCodeInvalid        = "verification_code_invalid"
	errorCodeVerificationCodeExpired        = "verification_code_expired"
	errorCodeVerificationAttemptsExceeded   = "verification_attempts_exceeded"
	errorCodeContactAlreadyRegistered       = "contact_already_registered"
	errorCodeInvalidCredentials             = "invalid_credentials"
	errorCodeInvalidPasswordResetRequest    = "invalid_password_reset_request"
	errorCodeAuthenticationRequired         = "authentication_required"
	errorCodeAnswerOwnerCapacityExhausted   = "answer_owner_capacity_exhausted"
	errorCodeAnswerCapacityExhausted        = "answer_capacity_exhausted"
	errorCodeInternal                       = "internal_error"
)

// errorResponse 是返回给 HTTP 调用方的安全错误契约。
//
// Error 适合直接展示或辅助开发；Code 是稳定的程序判断字段，前端不需要依赖
// 可能调整措辞的英文消息。迁移期间尚未提供 Code 的旧接口会因 omitempty 保持原响应。
type errorResponse struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

// writeErrorResponse 写入可预期的安全错误，不额外记录内部 ERROR 日志。
// 400、404、409 等状态已经由 HTTP 访问日志统一记录。
func writeErrorResponse(
	c *gin.Context,
	statusCode int,
	errorCode string,
	message string,
) {
	c.JSON(statusCode, errorResponse{
		Error: message,
		Code:  errorCode,
	})
}

// writeInternalErrorResponse 同时完成两件不同的工作：
// 1. 后端日志保存原始错误和诊断字段；
// 2. 前端只收到稳定、安全的通用 500 响应。
func writeInternalErrorResponse(
	c *gin.Context,
	logger *slog.Logger,
	diagnosticCode string,
	err error,
	additionalAttributes ...slog.Attr,
) {
	requestID, _ := observability.RequestIDFromContext(
		c.Request.Context(),
	)

	attributes := []slog.Attr{
		slog.String("event", "http_request_failed"),
		slog.String("request_id", requestID),
		slog.String("public_error_code", errorCodeInternal),
		slog.String("diagnostic_code", diagnosticCode),
		slog.Any("error", err),
	}
	attributes = append(attributes, additionalAttributes...)

	logger.LogAttrs(
		c.Request.Context(),
		slog.LevelError,
		"HTTP request failed",
		attributes...,
	)

	writeErrorResponse(
		c,
		http.StatusInternalServerError,
		errorCodeInternal,
		"internal server error",
	)
}
