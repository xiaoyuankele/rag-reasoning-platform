package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	agentapplication "rag-reasoning-platform/backend/internal/application/agent"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

const maxJSONSafeInteger int64 = 1<<53 - 1

var (
	agentEvidenceRefPattern = regexp.MustCompile(`^step-([1-9][0-9]*):item-([1-9][0-9]*)$`)
	agentContentHashPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type agentTokenUsageResponse struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type agentExecutionErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type agentSourceResponse struct {
	Citation    int    `json:"citation"`
	EvidenceRef string `json:"evidence_ref"`
	DocumentID  int64  `json:"document_id"`
	ChunkID     int64  `json:"chunk_id"`
	Title       string `json:"title"`
	PageStart   *int   `json:"page_start"`
	PageEnd     *int   `json:"page_end"`
	ContentHash string `json:"content_hash"`
}

type agentResultResponse struct {
	Answer           string                `json:"answer"`
	ResponseLanguage string                `json:"response_language"`
	Sources          []agentSourceResponse `json:"sources"`
}

type agentStepResponse struct {
	ID          int64                        `json:"id"`
	AgentRunID  int64                        `json:"agent_run_id"`
	SequenceNo  int                          `json:"sequence_no"`
	Kind        string                       `json:"kind"`
	Status      string                       `json:"status"`
	ToolName    *string                      `json:"tool_name"`
	Error       *agentExecutionErrorResponse `json:"error"`
	TokenUsage  agentTokenUsageResponse      `json:"token_usage"`
	CreatedAt   time.Time                    `json:"created_at"`
	StartedAt   *time.Time                   `json:"started_at"`
	CompletedAt *time.Time                   `json:"completed_at"`
}

type agentRunResponse struct {
	ID                        int64                        `json:"id"`
	ConversationID            int64                        `json:"conversation_id"`
	UserMessageID             int64                        `json:"user_message_id"`
	AssistantMessageID        int64                        `json:"assistant_message_id"`
	ClientRunID               string                       `json:"client_run_id"`
	RequestedDocumentID       *int64                       `json:"requested_document_id"`
	RequestedResponseLanguage string                       `json:"requested_response_language"`
	Status                    string                       `json:"status"`
	StepCount                 int                          `json:"step_count"`
	MaxSteps                  int                          `json:"max_steps"`
	MaxTotalTokens            int                          `json:"max_total_tokens"`
	Cancelable                bool                         `json:"cancelable"`
	Steps                     []agentStepResponse          `json:"steps"`
	Result                    *agentResultResponse         `json:"result"`
	Error                     *agentExecutionErrorResponse `json:"error"`
	TokenUsage                agentTokenUsageResponse      `json:"token_usage"`
	CreatedAt                 time.Time                    `json:"created_at"`
	UpdatedAt                 time.Time                    `json:"updated_at"`
	StartedAt                 *time.Time                   `json:"started_at"`
	CompletedAt               *time.Time                   `json:"completed_at"`
}

type agentRunEnvelopeResponse struct {
	AgentRun agentRunResponse `json:"agent_run"`
}

type createAgentRunResponse struct {
	ConversationID   int64                       `json:"conversation_id"`
	UserMessage      conversationMessageResponse `json:"user_message"`
	AssistantMessage conversationMessageResponse `json:"assistant_message"`
	AgentRun         agentRunResponse            `json:"agent_run"`
}

type persistedAgentFinalPayload struct {
	Sources []persistedAgentSource `json:"sources"`
}

type persistedAgentSource struct {
	EvidenceRef string          `json:"evidence_ref"`
	DocumentID  int64           `json:"document_id"`
	ChunkID     int64           `json:"chunk_id"`
	Title       json.RawMessage `json:"title"`
	PageStart   json.RawMessage `json:"page_start"`
	PageEnd     json.RawMessage `json:"page_end"`
	ContentHash string          `json:"content_hash"`
}

type persistedAgentSearchResult struct {
	Items []persistedAgentSearchItem `json:"items"`
}

type persistedAgentSearchItem struct {
	EvidenceRef string  `json:"evidence_ref"`
	DocumentID  int64   `json:"document_id"`
	ChunkID     int64   `json:"chunk_id"`
	Title       string  `json:"title"`
	PageStart   *int    `json:"page_start"`
	PageEnd     *int    `json:"page_end"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
}

type persistedAgentReadResult struct {
	Items []persistedAgentReadItem `json:"items"`
}

type persistedAgentReadArguments struct {
	EvidenceRefs []string `json:"evidence_refs"`
}

type persistedAgentReadItem struct {
	EvidenceRef string `json:"evidence_ref"`
	DocumentID  int64  `json:"document_id"`
	ChunkID     int64  `json:"chunk_id"`
	Title       string `json:"title"`
	PageStart   *int   `json:"page_start"`
	PageEnd     *int   `json:"page_end"`
	Excerpt     string `json:"excerpt"`
	ContentHash string `json:"content_hash"`
}

type trustedAgentSource struct {
	EvidenceRef string
	DocumentID  int64
	ChunkID     int64
	Title       string
	PageStart   *int
	PageEnd     *int
	ContentHash string
}

func newAgentRunResponse(
	expectedOwnerID int64,
	snapshot agentapplication.RunSnapshot,
) (agentRunResponse, error) {
	run := snapshot.Run
	if !validPositiveJSONInt64(expectedOwnerID) || run.OwnerUserID != expectedOwnerID ||
		!validPositiveJSONInt64(run.ID) || !validPositiveJSONInt64(run.ConversationID) ||
		!validPositiveJSONInt64(run.UserMessageID) || !validPositiveJSONInt64(run.AssistantMessageID) ||
		run.UserMessageID == run.AssistantMessageID ||
		!run.Status.IsValid() || !run.ResponseLanguage.IsValid() ||
		!validAgentClientRunID(run.ClientRunID) || snapshot.Steps == nil {
		return agentRunResponse{}, errors.New("agent Run snapshot identity is invalid")
	}
	if run.RequestedDocumentID != nil && !validPositiveJSONInt64(*run.RequestedDocumentID) {
		return agentRunResponse{}, errors.New("agent Run requested document is invalid")
	}
	if run.Budget.MaxSteps <= 0 || run.Budget.MaxSteps > agentdomain.MaxPersistedSteps ||
		run.Budget.MaxTotalTokens <= 0 || run.Budget.MaxTotalTokens > agentdomain.MaxRunTotalTokens ||
		run.Budget.DeadlineAt.IsZero() || run.StepCount < 0 || run.StepCount > run.Budget.MaxSteps ||
		run.StepCount != len(snapshot.Steps) || run.AttemptCount < 0 ||
		run.AttemptCount > agentdomain.MaxAutomaticAttempts {
		return agentRunResponse{}, errors.New("agent Run snapshot budget or count is invalid")
	}
	if run.WorkerID != "" || run.LeaseToken != "" || run.LeaseExpiresAt != nil || run.HeartbeatAt != nil {
		return agentRunResponse{}, errors.New("agent Run snapshot contains worker internals")
	}
	if !validAgentRunTimes(run) || !validAgentTokenUsage(run.PromptTokens, run.CompletionTokens, run.TotalTokens) ||
		run.TotalTokens > run.Budget.MaxTotalTokens {
		return agentRunResponse{}, errors.New("agent Run snapshot timing or Token usage is invalid")
	}
	if !validAgentStoredError(run.ErrorCode, run.ErrorMessage) {
		return agentRunResponse{}, errors.New("agent Run snapshot stored error is invalid")
	}

	steps := make([]agentStepResponse, 0, len(snapshot.Steps))
	promptTokens := int64(0)
	completionTokens := int64(0)
	totalTokens := int64(0)
	finalCount := 0
	var finalStepResult agentapplication.FinalStepResult
	seenStepIDs := make(map[int64]struct{}, len(snapshot.Steps))
	for index, step := range snapshot.Steps {
		if _, duplicate := seenStepIDs[step.ID]; duplicate {
			return agentRunResponse{}, errors.New("agent Step ID is duplicated")
		}
		seenStepIDs[step.ID] = struct{}{}
		mapped, parsedFinal, err := newAgentStepResponse(run, step, index+1)
		if err != nil {
			return agentRunResponse{}, err
		}
		if parsedFinal != nil {
			finalCount++
			finalStepResult = *parsedFinal
			if index != len(snapshot.Steps)-1 {
				return agentRunResponse{}, errors.New("agent final Step is not last")
			}
		}
		if run.Status.IsTerminal() && !step.Status.IsTerminal() {
			return agentRunResponse{}, errors.New("terminal agent Run contains an active Step")
		}
		if run.Status == agentdomain.RunStatusSucceeded && step.Status == agentdomain.StepStatusFailed {
			return agentRunResponse{}, errors.New("succeeded agent Run contains a failed Step")
		}
		promptTokens += int64(step.PromptTokens)
		completionTokens += int64(step.CompletionTokens)
		totalTokens += int64(step.TotalTokens)
		if promptTokens > maxJSONSafeInteger || completionTokens > maxJSONSafeInteger || totalTokens > maxJSONSafeInteger {
			return agentRunResponse{}, errors.New("agent Step Token aggregate is unsafe")
		}
		steps = append(steps, mapped)
	}
	if promptTokens != int64(run.PromptTokens) || completionTokens != int64(run.CompletionTokens) ||
		totalTokens != int64(run.TotalTokens) {
		return agentRunResponse{}, errors.New("agent Run Token usage does not equal its Steps")
	}

	var result *agentResultResponse
	var publicError *agentExecutionErrorResponse
	if run.Status != agentdomain.RunStatusQueued && run.Status != agentdomain.RunStatusFailed &&
		run.ErrorCode != agentdomain.ErrorCodeNone {
		return agentRunResponse{}, errors.New("non-failed agent Run contains an error")
	}
	switch run.Status {
	case agentdomain.RunStatusSucceeded:
		if run.Result == nil || finalCount != 1 || !validAgentRunResult(run, finalStepResult) {
			return agentRunResponse{}, errors.New("succeeded agent Run result is invalid")
		}
		mappedResult, err := newAgentResultResponse(*run.Result, snapshot.Steps)
		if err != nil {
			return agentRunResponse{}, err
		}
		result = &mappedResult
	case agentdomain.RunStatusFailed:
		if run.Result != nil || finalCount != 0 {
			return agentRunResponse{}, errors.New("failed agent Run contains a final result")
		}
		mappedError, ok := newAgentExecutionErrorResponse(run.ErrorCode)
		if !ok {
			return agentRunResponse{}, errors.New("failed agent Run error code is invalid")
		}
		publicError = &mappedError
	default:
		if run.Result != nil || finalCount != 0 {
			return agentRunResponse{}, errors.New("non-succeeded agent Run contains a final result")
		}
	}

	return agentRunResponse{
		ID: run.ID, ConversationID: run.ConversationID,
		UserMessageID: run.UserMessageID, AssistantMessageID: run.AssistantMessageID,
		ClientRunID: run.ClientRunID, RequestedDocumentID: copyAgentInt64(run.RequestedDocumentID),
		RequestedResponseLanguage: string(run.ResponseLanguage), Status: string(run.Status),
		StepCount: run.StepCount, MaxSteps: run.Budget.MaxSteps, MaxTotalTokens: run.Budget.MaxTotalTokens,
		Cancelable: run.Status == agentdomain.RunStatusQueued, Steps: steps, Result: result, Error: publicError,
		TokenUsage: agentTokenUsageResponse{
			PromptTokens: run.PromptTokens, CompletionTokens: run.CompletionTokens, TotalTokens: run.TotalTokens,
		},
		CreatedAt: run.CreatedAt.UTC(), UpdatedAt: run.UpdatedAt.UTC(),
		StartedAt: copyAgentTime(run.StartedAt), CompletedAt: copyAgentTime(run.CompletedAt),
	}, nil
}

func newAgentStepResponse(
	run agentdomain.Run,
	step agentdomain.Step,
	wantSequence int,
) (agentStepResponse, *agentapplication.FinalStepResult, error) {
	if !validPositiveJSONInt64(step.ID) || step.RunID != run.ID ||
		step.SequenceNo != wantSequence || !validPositiveJSONInt(step.SequenceNo) ||
		!step.Kind.IsValid() || !step.Status.IsValid() || !validAgentStepTimes(step) ||
		!validAgentTokenUsage(step.PromptTokens, step.CompletionTokens, step.TotalTokens) ||
		!validAgentStoredError(step.ErrorCode, step.ErrorMessage) {
		return agentStepResponse{}, nil, errors.New("agent Step snapshot is invalid")
	}

	var finalResult *agentapplication.FinalStepResult
	switch step.Kind {
	case agentdomain.StepKindToolCall:
		if step.ToolName == nil ||
			(*step.ToolName != agentapplication.ToolSearchDocuments &&
				*step.ToolName != agentapplication.ToolReadDocumentEvidence) ||
			len(step.Arguments) == 0 || !json.Valid(step.Arguments) {
			return agentStepResponse{}, nil, errors.New("agent Tool Step shape is invalid")
		}
		if step.Status == agentdomain.StepStatusSucceeded {
			if len(step.Result) == 0 || !json.Valid(step.Result) {
				return agentStepResponse{}, nil, errors.New("successful agent Tool Step result is invalid")
			}
		} else if len(step.Result) != 0 {
			return agentStepResponse{}, nil, errors.New("non-successful agent Tool Step contains a result")
		}
	case agentdomain.StepKindFinal:
		if step.Status != agentdomain.StepStatusSucceeded || step.ToolName != nil ||
			len(step.Arguments) != 0 || len(step.Result) == 0 {
			return agentStepResponse{}, nil, errors.New("agent final Step shape is invalid")
		}
		parsed, err := decodeAgentFinalStepResult(step.Result)
		if err != nil {
			return agentStepResponse{}, nil, err
		}
		finalResult = &parsed
	}

	var publicError *agentExecutionErrorResponse
	if step.Status == agentdomain.StepStatusFailed {
		mapped, ok := newAgentExecutionErrorResponse(step.ErrorCode)
		if !ok {
			return agentStepResponse{}, nil, errors.New("failed agent Step error code is invalid")
		}
		publicError = &mapped
	} else if step.ErrorCode != agentdomain.ErrorCodeNone {
		return agentStepResponse{}, nil, errors.New("non-failed agent Step contains an error")
	}

	return agentStepResponse{
		ID: step.ID, AgentRunID: step.RunID, SequenceNo: step.SequenceNo,
		Kind: string(step.Kind), Status: string(step.Status), ToolName: copyAgentString(step.ToolName),
		Error: publicError,
		TokenUsage: agentTokenUsageResponse{
			PromptTokens: step.PromptTokens, CompletionTokens: step.CompletionTokens, TotalTokens: step.TotalTokens,
		},
		CreatedAt: step.CreatedAt.UTC(), StartedAt: copyAgentTime(step.StartedAt), CompletedAt: copyAgentTime(step.CompletedAt),
	}, finalResult, nil
}

func newAgentResultResponse(
	result agentdomain.Result,
	steps []agentdomain.Step,
) (agentResultResponse, error) {
	if strings.TrimSpace(result.Answer) == "" || !utf8.ValidString(result.Answer) ||
		!result.ResponseLanguage.IsResolved() ||
		!validAgentTokenUsage(result.PromptTokens, result.CompletionTokens, result.TotalTokens) {
		return agentResultResponse{}, errors.New("agent result is invalid")
	}
	var payload persistedAgentFinalPayload
	if err := decodeStrictAgentJSONObject(result.Payload, &payload); err != nil ||
		len(payload.Sources) == 0 || len(payload.Sources) > agentapplication.MaxReadEvidenceRefs {
		return agentResultResponse{}, errors.New("agent final sources payload is invalid")
	}
	searchSources, readSources, err := buildTrustedAgentSources(steps)
	if err != nil {
		return agentResultResponse{}, err
	}

	seen := make(map[string]struct{}, len(payload.Sources))
	sources := make([]agentSourceResponse, 0, len(payload.Sources))
	for index, source := range payload.Sources {
		matches := agentEvidenceRefPattern.FindStringSubmatch(source.EvidenceRef)
		if len(matches) != 3 || !validPositiveJSONInt64(source.DocumentID) ||
			!validPositiveJSONInt64(source.ChunkID) || !agentContentHashPattern.MatchString(source.ContentHash) {
			return agentResultResponse{}, errors.New("agent final source identity is invalid")
		}
		if _, duplicate := seen[source.EvidenceRef]; duplicate {
			return agentResultResponse{}, errors.New("agent final source is duplicated")
		}
		seen[source.EvidenceRef] = struct{}{}
		sequenceNo, sequenceErr := strconv.Atoi(matches[1])
		itemNo, itemErr := strconv.Atoi(matches[2])
		if sequenceErr != nil || itemErr != nil || sequenceNo <= 0 || itemNo <= 0 {
			return agentResultResponse{}, errors.New("agent final evidence reference is invalid")
		}
		title, err := decodeAgentOptionalTitle(source.Title)
		if err != nil {
			return agentResultResponse{}, err
		}
		pageStart, err := decodeAgentOptionalPage(source.PageStart)
		if err != nil {
			return agentResultResponse{}, err
		}
		pageEnd, err := decodeAgentOptionalPage(source.PageEnd)
		if err != nil || !validAgentPageRange(pageStart, pageEnd) {
			return agentResultResponse{}, errors.New("agent final source page range is invalid")
		}
		trustedSearch, found := searchSources[source.EvidenceRef]
		if !found || trustedSearch.DocumentID != source.DocumentID ||
			trustedSearch.ChunkID != source.ChunkID || trustedSearch.Title != title ||
			!equalAgentOptionalInt(trustedSearch.PageStart, pageStart) ||
			!equalAgentOptionalInt(trustedSearch.PageEnd, pageEnd) ||
			trustedSearch.EvidenceRef != fmt.Sprintf("step-%d:item-%d", sequenceNo, itemNo) {
			return agentResultResponse{}, errors.New("agent final source does not match its search evidence")
		}
		trustedRead, found := readSources[source.EvidenceRef]
		if !found || trustedRead.DocumentID != source.DocumentID ||
			trustedRead.ChunkID != source.ChunkID || trustedRead.Title != title ||
			!equalAgentOptionalInt(trustedRead.PageStart, pageStart) ||
			!equalAgentOptionalInt(trustedRead.PageEnd, pageEnd) ||
			trustedRead.ContentHash != source.ContentHash {
			return agentResultResponse{}, errors.New("agent final source does not match its read evidence")
		}
		sources = append(sources, agentSourceResponse{
			Citation: index + 1, EvidenceRef: source.EvidenceRef,
			DocumentID: source.DocumentID, ChunkID: source.ChunkID, Title: title,
			PageStart: pageStart, PageEnd: pageEnd, ContentHash: source.ContentHash,
		})
	}
	return agentResultResponse{
		Answer: result.Answer, ResponseLanguage: string(result.ResponseLanguage), Sources: sources,
	}, nil
}

func buildTrustedAgentSources(
	steps []agentdomain.Step,
) (map[string]trustedAgentSource, map[string]trustedAgentSource, error) {
	searchSources := make(map[string]trustedAgentSource)
	readSources := make(map[string]trustedAgentSource)
	for _, step := range steps {
		if step.Kind != agentdomain.StepKindToolCall || step.Status != agentdomain.StepStatusSucceeded || step.ToolName == nil {
			continue
		}
		switch *step.ToolName {
		case agentapplication.ToolSearchDocuments:
			var result persistedAgentSearchResult
			if err := decodeStrictAgentJSONObject(step.Result, &result); err != nil {
				return nil, nil, errors.New("agent search Step result is invalid")
			}
			for index, item := range result.Items {
				expectedRef := fmt.Sprintf("step-%d:item-%d", step.SequenceNo, index+1)
				if item.EvidenceRef != expectedRef || !validPositiveJSONInt64(item.DocumentID) ||
					!validPositiveJSONInt64(item.ChunkID) || !utf8.ValidString(item.Title) ||
					!utf8.ValidString(item.Snippet) || strings.TrimSpace(item.Snippet) == "" ||
					math.IsNaN(item.Score) || math.IsInf(item.Score, 0) ||
					!validAgentPageRange(item.PageStart, item.PageEnd) {
					return nil, nil, errors.New("agent search evidence is invalid")
				}
				if _, duplicate := searchSources[item.EvidenceRef]; duplicate {
					return nil, nil, errors.New("agent search evidence is duplicated")
				}
				searchSources[item.EvidenceRef] = trustedAgentSource{
					EvidenceRef: item.EvidenceRef, DocumentID: item.DocumentID, ChunkID: item.ChunkID,
					Title: item.Title, PageStart: item.PageStart, PageEnd: item.PageEnd,
				}
			}
		case agentapplication.ToolReadDocumentEvidence:
			var arguments persistedAgentReadArguments
			if err := decodeStrictAgentJSONObject(step.Arguments, &arguments); err != nil ||
				len(arguments.EvidenceRefs) == 0 || len(arguments.EvidenceRefs) > agentapplication.MaxReadEvidenceRefs {
				return nil, nil, errors.New("agent read-evidence Step arguments are invalid")
			}
			var result persistedAgentReadResult
			if err := decodeStrictAgentJSONObject(step.Result, &result); err != nil ||
				len(result.Items) != len(arguments.EvidenceRefs) {
				return nil, nil, errors.New("agent read-evidence Step result is invalid")
			}
			seenArguments := make(map[string]struct{}, len(arguments.EvidenceRefs))
			for index, item := range result.Items {
				if item.EvidenceRef != arguments.EvidenceRefs[index] {
					return nil, nil, errors.New("agent read evidence order is invalid")
				}
				if _, duplicate := seenArguments[item.EvidenceRef]; duplicate {
					return nil, nil, errors.New("agent read evidence argument is duplicated")
				}
				seenArguments[item.EvidenceRef] = struct{}{}
				if !agentEvidenceRefPattern.MatchString(item.EvidenceRef) ||
					!validPositiveJSONInt64(item.DocumentID) || !validPositiveJSONInt64(item.ChunkID) ||
					!utf8.ValidString(item.Title) || !utf8.ValidString(item.Excerpt) ||
					strings.TrimSpace(item.Excerpt) == "" || !validAgentPageRange(item.PageStart, item.PageEnd) ||
					!agentContentHashPattern.MatchString(item.ContentHash) {
					return nil, nil, errors.New("agent read evidence is invalid")
				}
				searched, found := searchSources[item.EvidenceRef]
				if !found || searched.DocumentID != item.DocumentID || searched.ChunkID != item.ChunkID ||
					searched.Title != item.Title || !equalAgentOptionalInt(searched.PageStart, item.PageStart) ||
					!equalAgentOptionalInt(searched.PageEnd, item.PageEnd) {
					return nil, nil, errors.New("agent read evidence does not match an earlier search Step")
				}
				trusted := trustedAgentSource{
					EvidenceRef: item.EvidenceRef, DocumentID: item.DocumentID, ChunkID: item.ChunkID,
					Title: item.Title, PageStart: item.PageStart, PageEnd: item.PageEnd,
					ContentHash: item.ContentHash,
				}
				if previous, duplicate := readSources[item.EvidenceRef]; duplicate && !reflect.DeepEqual(previous, trusted) {
					return nil, nil, errors.New("agent read evidence conflicts")
				}
				readSources[item.EvidenceRef] = trusted
			}
		}
	}
	return searchSources, readSources, nil
}

func decodeAgentFinalStepResult(raw json.RawMessage) (agentapplication.FinalStepResult, error) {
	var result agentapplication.FinalStepResult
	if err := decodeStrictAgentJSONObject(raw, &result); err != nil ||
		strings.TrimSpace(result.Answer) == "" || !utf8.ValidString(result.Answer) ||
		!result.ResponseLanguage.IsResolved() || len(result.Payload) == 0 {
		return agentapplication.FinalStepResult{}, errors.New("agent final Step result is invalid")
	}
	var payload persistedAgentFinalPayload
	if err := decodeStrictAgentJSONObject(result.Payload, &payload); err != nil {
		return agentapplication.FinalStepResult{}, errors.New("agent final Step payload is invalid")
	}
	return result, nil
}

func validAgentRunResult(run agentdomain.Run, final agentapplication.FinalStepResult) bool {
	return run.Result != nil && run.Result.IsValid() &&
		run.Result.PromptTokens == run.PromptTokens &&
		run.Result.CompletionTokens == run.CompletionTokens &&
		run.Result.TotalTokens == run.TotalTokens &&
		final.Answer == run.Result.Answer && final.ResponseLanguage == run.Result.ResponseLanguage &&
		agentJSONSemanticallyEqual(final.Payload, run.Result.Payload)
}

func newAgentExecutionErrorResponse(code agentdomain.ErrorCode) (agentExecutionErrorResponse, bool) {
	messages := map[agentdomain.ErrorCode]string{
		agentdomain.ErrorCodeExecutionFailed:        "Agent execution failed",
		agentdomain.ErrorCodeTemporarilyUnavailable: "Agent is temporarily unavailable",
		agentdomain.ErrorCodeStepLimitExceeded:      "Agent step limit exceeded",
		agentdomain.ErrorCodeTokenBudgetExceeded:    "Agent token budget exceeded",
		agentdomain.ErrorCodeDeadlineExceeded:       "Agent execution deadline exceeded",
		agentdomain.ErrorCodeInvalidToolCall:        "Agent produced an invalid tool call",
		agentdomain.ErrorCodeToolFailed:             "Agent tool execution failed",
		agentdomain.ErrorCodeWorkerInterrupted:      "Agent worker was interrupted",
	}
	message, ok := messages[code]
	if !ok {
		return agentExecutionErrorResponse{}, false
	}
	return agentExecutionErrorResponse{Code: string(code), Message: message}, true
}

func validAgentStoredError(code agentdomain.ErrorCode, message *string) bool {
	if code == agentdomain.ErrorCodeNone {
		return message == nil
	}
	if !code.IsValid() || message == nil || strings.TrimSpace(*message) == "" || !utf8.ValidString(*message) {
		return false
	}
	_, safe := newAgentExecutionErrorResponse(code)
	return safe
}

func validAgentTokenUsage(prompt, completion, total int) bool {
	if !agentdomain.TokenUsageIsValid(prompt, completion, total) {
		return false
	}
	return int64(prompt) <= maxJSONSafeInteger && int64(completion) <= maxJSONSafeInteger &&
		int64(total) <= maxJSONSafeInteger
}

func validAgentRunTimes(run agentdomain.Run) bool {
	if !validAgentJSONTime(run.CreatedAt) || !validAgentJSONTime(run.UpdatedAt) || run.UpdatedAt.Before(run.CreatedAt) ||
		!validAgentOptionalTime(run.StartedAt) || !validAgentOptionalTime(run.CompletedAt) {
		return false
	}
	if run.StartedAt != nil && (run.StartedAt.Before(run.CreatedAt) || run.UpdatedAt.Before(*run.StartedAt)) {
		return false
	}
	if run.CompletedAt != nil && (run.CompletedAt.Before(run.CreatedAt) || run.UpdatedAt.Before(*run.CompletedAt) ||
		(run.StartedAt != nil && run.CompletedAt.Before(*run.StartedAt))) {
		return false
	}
	if run.Status.IsTerminal() != (run.CompletedAt != nil) {
		return false
	}
	if (run.AttemptCount == 0) != (run.StartedAt == nil) {
		return false
	}
	if (run.Status == agentdomain.RunStatusProcessing || run.Status == agentdomain.RunStatusSucceeded ||
		run.Status == agentdomain.RunStatusFailed || run.AttemptCount > 0) && run.StartedAt == nil {
		return false
	}
	return true
}

func validAgentStepTimes(step agentdomain.Step) bool {
	if !validAgentJSONTime(step.CreatedAt) || !validAgentOptionalTime(step.StartedAt) ||
		!validAgentOptionalTime(step.CompletedAt) {
		return false
	}
	if step.StartedAt != nil && step.StartedAt.Before(step.CreatedAt) {
		return false
	}
	if step.CompletedAt != nil && (step.CompletedAt.Before(step.CreatedAt) ||
		(step.StartedAt != nil && step.CompletedAt.Before(*step.StartedAt))) {
		return false
	}
	switch step.Status {
	case agentdomain.StepStatusPending:
		return step.StartedAt == nil && step.CompletedAt == nil
	case agentdomain.StepStatusRunning:
		return step.StartedAt != nil && step.CompletedAt == nil
	case agentdomain.StepStatusSucceeded, agentdomain.StepStatusFailed:
		return step.StartedAt != nil && step.CompletedAt != nil
	default:
		return false
	}
}

func newCreateAgentRunResponse(
	conversationID int64,
	expectedOwnerID int64,
	request createAgentRunRequest,
	result agentapplication.QueueResult,
) (createAgentRunResponse, error) {
	if !validPositiveJSONInt64(conversationID) || result.Run.ConversationID != conversationID ||
		result.Run.ClientRunID != strings.TrimSpace(request.ClientRunID) ||
		!equalAgentOptionalInt64(result.Run.RequestedDocumentID, request.DocumentID) ||
		result.UserMessage.Content != request.Content {
		return createAgentRunResponse{}, errors.New("created agent Run does not match its request")
	}
	if result.Created && result.Run.Status != agentdomain.RunStatusQueued {
		return createAgentRunResponse{}, errors.New("newly created agent Run is not queued")
	}
	wantLanguage := agentdomain.ResponseLanguageAuto
	if request.ResponseLanguage != nil {
		wantLanguage = agentdomain.ResponseLanguage(strings.ToLower(strings.TrimSpace(*request.ResponseLanguage)))
		if wantLanguage == "" {
			wantLanguage = agentdomain.ResponseLanguageAuto
		}
	}
	if result.Run.ResponseLanguage != wantLanguage {
		return createAgentRunResponse{}, errors.New("created agent Run language does not match its request")
	}
	if err := validateAgentQueueMessages(result); err != nil {
		return createAgentRunResponse{}, err
	}
	mappedRun, err := newAgentRunResponse(expectedOwnerID, result.RunSnapshot)
	if err != nil {
		return createAgentRunResponse{}, err
	}
	return createAgentRunResponse{
		ConversationID:   conversationID,
		UserMessage:      newConversationMessageResponse(result.UserMessage),
		AssistantMessage: newConversationMessageResponse(result.AssistantMessage),
		AgentRun:         mappedRun,
	}, nil
}

func validateAgentQueueMessages(result agentapplication.QueueResult) error {
	user := result.UserMessage
	assistant := result.AssistantMessage
	if !validPositiveJSONInt64(user.ID) || !validPositiveJSONInt64(assistant.ID) ||
		user.ID != result.Run.UserMessageID || assistant.ID != result.Run.AssistantMessageID ||
		user.ConversationID != result.Run.ConversationID || assistant.ConversationID != result.Run.ConversationID ||
		user.Role != conversationdomain.RoleUser || assistant.Role != conversationdomain.RoleAssistant ||
		user.Status != conversationdomain.MessageStatusCompleted ||
		user.SequenceNo <= 0 || assistant.SequenceNo != user.SequenceNo+1 ||
		!validPositiveJSONInt64(user.SequenceNo) || !validPositiveJSONInt64(assistant.SequenceNo) ||
		!utf8.ValidString(user.Content) || !utf8.ValidString(assistant.Content) ||
		strings.TrimSpace(user.Content) == "" ||
		!validAgentMessageTimes(user) || !validAgentMessageTimes(assistant) {
		return errors.New("created agent Run messages are invalid")
	}
	if (user.ClientMessageID != nil && !utf8.ValidString(*user.ClientMessageID)) ||
		(assistant.ClientMessageID != nil && !utf8.ValidString(*assistant.ClientMessageID)) {
		return errors.New("created agent message client ID is invalid")
	}
	wantAssistantStatus := conversationdomain.MessageStatusGenerating
	switch result.Run.Status {
	case agentdomain.RunStatusSucceeded:
		wantAssistantStatus = conversationdomain.MessageStatusCompleted
		if result.Run.Result == nil || assistant.Content != result.Run.Result.Answer {
			return errors.New("created agent assistant message does not match the final result")
		}
	case agentdomain.RunStatusFailed:
		wantAssistantStatus = conversationdomain.MessageStatusFailed
	case agentdomain.RunStatusCanceled:
		wantAssistantStatus = conversationdomain.MessageStatusInterrupted
	default:
		if assistant.Content != conversationdomain.AssistantGeneratingPlaceholder {
			return errors.New("active agent assistant message is not the generating placeholder")
		}
	}
	if assistant.Status != wantAssistantStatus || strings.TrimSpace(assistant.Content) == "" {
		return errors.New("created agent assistant message status is inconsistent")
	}
	return nil
}

func validAgentMessageTimes(message conversationdomain.Message) bool {
	return validAgentJSONTime(message.CreatedAt) && validAgentJSONTime(message.UpdatedAt) &&
		!message.UpdatedAt.Before(message.CreatedAt)
}

func decodeAgentOptionalTitle(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", errors.New("agent final source title must be a string")
	}
	var title string
	if err := json.Unmarshal(raw, &title); err != nil || !utf8.ValidString(title) {
		return "", errors.New("agent final source title is invalid")
	}
	return title, nil
}

func decodeAgentOptionalPage(raw json.RawMessage) (*int, error) {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var page int
	if err := json.Unmarshal(raw, &page); err != nil || page <= 0 || !validPositiveJSONInt(page) {
		return nil, errors.New("agent final source page is invalid")
	}
	return &page, nil
}

func validAgentPageRange(start, end *int) bool {
	if start == nil || end == nil {
		return start == nil && end == nil
	}
	return *start > 0 && *start <= *end && validPositiveJSONInt(*end)
}

func validAgentClientRunID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && utf8.ValidString(value) &&
		utf8.RuneCountInString(value) <= agentapplication.MaxClientRunIDLength
}

func validPositiveJSONInt(value int) bool {
	return value > 0
}

func validPositiveJSONInt64(value int64) bool {
	return value > 0
}

func validAgentJSONTime(value time.Time) bool {
	return !value.IsZero() && value.Year() >= 0 && value.Year() <= 9999
}

func validAgentOptionalTime(value *time.Time) bool {
	return value == nil || validAgentJSONTime(*value)
}

func copyAgentTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := value.UTC()
	return &copy
}

func copyAgentInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func copyAgentString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func equalAgentOptionalInt(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalAgentOptionalInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func agentJSONSemanticallyEqual(left, right json.RawMessage) bool {
	if len(left) == 0 || len(right) == 0 {
		return len(left) == 0 && len(right) == 0
	}
	var leftValue any
	var rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func decodeStrictAgentJSONObject(raw []byte, destination any) error {
	if !utf8.Valid(raw) || !validAgentJSONUnicodeEscapes(raw) {
		return errors.New("JSON must contain valid Unicode")
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("JSON body must be an object")
	}
	if err := validateAgentJSONHasUniqueKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON body must contain one value")
		}
		return err
	}
	return nil
}

func validateAgentJSONHasUniqueKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := validateAgentJSONValueKeys(decoder); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		if err == nil {
			return errors.New("JSON body must contain one value")
		}
		return err
	}
	return nil
}

func validateAgentJSONValueKeys(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is invalid")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON object key %q is duplicated", key)
			}
			seen[key] = struct{}{}
			if err := validateAgentJSONValueKeys(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("JSON object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := validateAgentJSONValueKeys(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("JSON array is not closed")
		}
	default:
		return errors.New("JSON delimiter is invalid")
	}
	return nil
}

func validAgentJSONUnicodeEscapes(raw []byte) bool {
	inString := false
	for index := 0; index < len(raw); index++ {
		current := raw[index]
		if !inString {
			if current == '"' {
				inString = true
			}
			continue
		}
		if current == '"' {
			inString = false
			continue
		}
		if current != '\\' {
			if current < 0x20 {
				return false
			}
			continue
		}
		index++
		if index >= len(raw) {
			return false
		}
		if raw[index] != 'u' {
			continue
		}
		codeUnit, ok := parseAgentJSONCodeUnit(raw, index+1)
		if !ok {
			return false
		}
		index += 4
		switch {
		case codeUnit >= 0xD800 && codeUnit <= 0xDBFF:
			if index+6 >= len(raw) || raw[index+1] != '\\' || raw[index+2] != 'u' {
				return false
			}
			low, ok := parseAgentJSONCodeUnit(raw, index+3)
			if !ok || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			index += 6
		case codeUnit >= 0xDC00 && codeUnit <= 0xDFFF:
			return false
		}
	}
	return !inString
}

func parseAgentJSONCodeUnit(raw []byte, start int) (uint16, bool) {
	if start+4 > len(raw) {
		return 0, false
	}
	value, err := strconv.ParseUint(string(raw[start:start+4]), 16, 16)
	return uint16(value), err == nil
}
