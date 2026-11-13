package answer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
)

const (
	defaultAnswerInputTokenBudget          = 16_000
	minimumConversationEvidenceTokenBudget = 2_000
)

var (
	// ErrConversationHistoryUnavailable 表示会话回答缺少历史读取能力。
	// standalone /answers 不携带会话标识，因此不会触发该错误。
	ErrConversationHistoryUnavailable = errors.New(
		"conversation history reader is unavailable",
	)
	// ErrInvalidConversationHistoryInput 表示会话和当前用户消息没有成对提供。
	ErrInvalidConversationHistoryInput = errors.New(
		"conversation history input is invalid",
	)
	// ErrAnswerInputTokenBudgetExceeded 表示即使只保留一条截断证据，
	// 当前问题和固定 Prompt 仍无法放入应用预算。
	ErrAnswerInputTokenBudgetExceeded = errors.New(
		"answer input token budget exceeded",
	)
)

// ConversationHistoryConfig 定义 M3.1 的有界历史和总输入预算。
// MaxTurns 与 MaxTokens 任一先耗尽就停止加入更早历史。
type ConversationHistoryConfig struct {
	MaxTurns         int
	MaxTokens        int
	InputTokenBudget int
}

// IsValid 保证历史不会占满整个 Prompt，至少给当前问题和文档证据保留空间。
func (c ConversationHistoryConfig) IsValid() bool {
	return c.MaxTurns > 0 && c.MaxTokens > 0 &&
		c.InputTokenBudget >= c.MaxTokens+minimumConversationEvidenceTokenBudget
}

// ConversationHistoryReader 是 AnswerService 读取当前消息之前历史所需的最小端口。
// 实现必须在 SQL 层同时校验 Owner、conversation 和 user message 关联。
type ConversationHistoryReader interface {
	ListCompletedMessagesBefore(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		conversationID int64,
		userMessageID int64,
		limit int,
	) ([]conversationdomain.Message, error)
}

type selectedConversationHistory struct {
	Messages        []conversationdomain.Message
	TurnCount       int
	EstimatedTokens int
	Truncated       bool
}

func (s *Service) loadConversationHistory(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
	language ResponseLanguage,
) (selectedConversationHistory, error) {
	if input.ConversationID == nil && input.UserMessageID == nil {
		return selectedConversationHistory{
			Messages: make([]conversationdomain.Message, 0),
		}, nil
	}
	if input.ConversationID == nil || input.UserMessageID == nil ||
		*input.ConversationID <= 0 || *input.UserMessageID <= 0 {
		return selectedConversationHistory{}, ErrInvalidConversationHistoryInput
	}
	if s.historyReader == nil || !s.historyConfig.IsValid() {
		return selectedConversationHistory{}, ErrConversationHistoryUnavailable
	}

	// 一轮最多包含 user + assistant。额外多取一轮用于判断是否发生裁剪；
	// standalone user-only 消息只会让候选更充足，不会突破后续双重预算。
	candidateLimit := s.historyConfig.MaxTurns*2 + 2
	messages, err := s.historyReader.ListCompletedMessagesBefore(
		ctx,
		scope,
		*input.ConversationID,
		*input.UserMessageID,
		candidateLimit,
	)
	if err != nil {
		return selectedConversationHistory{}, fmt.Errorf(
			"list completed conversation history: %w",
			err,
		)
	}

	return selectConversationHistory(
		messages,
		language,
		s.historyConfig.MaxTurns,
		s.historyConfig.MaxTokens,
	), nil
}

func selectConversationHistory(
	messages []conversationdomain.Message,
	language ResponseLanguage,
	maxTurns int,
	maxTokens int,
) selectedConversationHistory {
	turns := groupCompletedConversationTurns(messages)
	if len(turns) == 0 || maxTurns <= 0 || maxTokens <= 0 {
		return selectedConversationHistory{
			Messages:  make([]conversationdomain.Message, 0),
			Truncated: len(turns) > 0,
		}
	}

	selectedReversed := make([][]conversationdomain.Message, 0, min(maxTurns, len(turns)))
	headerTokens := estimateTextTokens(promptLabelsFor(language).history + "\n")
	remainingTokens := maxTokens - headerTokens
	if remainingTokens <= 0 {
		return selectedConversationHistory{
			Messages:  make([]conversationdomain.Message, 0),
			Truncated: true,
		}
	}
	truncated := len(turns) > maxTurns

	for turnIndex := len(turns) - 1; turnIndex >= 0 && len(selectedReversed) < maxTurns; turnIndex-- {
		turn := turns[turnIndex]
		turnTokens := estimateTextTokens(formatConversationHistory(turn, language))
		if turnTokens <= remainingTokens {
			selectedReversed = append(selectedReversed, turn)
			remainingTokens -= turnTokens
			continue
		}

		truncated = true
		if len(selectedReversed) == 0 && remainingTokens > 0 {
			partial := truncateHistoryTurn(turn, language, remainingTokens)
			if len(partial) > 0 {
				selectedReversed = append(selectedReversed, partial)
				remainingTokens -= estimateTextTokens(
					formatConversationHistory(partial, language),
				)
			}
		}
		break
	}

	selected := make([]conversationdomain.Message, 0, len(selectedReversed)*2)
	for index := len(selectedReversed) - 1; index >= 0; index-- {
		selected = append(selected, selectedReversed[index]...)
	}
	rendered := formatConversationHistory(selected, language)
	estimatedTokens := 0
	if rendered != "" {
		estimatedTokens = headerTokens + estimateTextTokens(rendered)
	}
	return selectedConversationHistory{
		Messages:        selected,
		TurnCount:       len(selectedReversed),
		EstimatedTokens: estimatedTokens,
		Truncated:       truncated || len(selectedReversed) < len(turns),
	}
}

func groupCompletedConversationTurns(
	messages []conversationdomain.Message,
) [][]conversationdomain.Message {
	turns := make([][]conversationdomain.Message, 0)
	for _, message := range messages {
		if message.Status != conversationdomain.MessageStatusCompleted {
			continue
		}
		switch message.Role {
		case conversationdomain.RoleUser:
			turns = append(turns, []conversationdomain.Message{message})
		case conversationdomain.RoleAssistant:
			if len(turns) > 0 {
				turns[len(turns)-1] = append(turns[len(turns)-1], message)
			}
		}
	}
	return turns
}

func truncateHistoryTurn(
	turn []conversationdomain.Message,
	language ResponseLanguage,
	tokenBudget int,
) []conversationdomain.Message {
	selected := make([]conversationdomain.Message, 0, len(turn))
	for _, message := range turn {
		candidate := append(append([]conversationdomain.Message(nil), selected...), message)
		if estimateTextTokens(formatConversationHistory(candidate, language)) <= tokenBudget {
			selected = append(selected, message)
			continue
		}

		truncatedContent := truncateHistoryMessageContent(
			selected,
			message,
			language,
			tokenBudget,
		)
		if truncatedContent != "" {
			message.Content = truncatedContent
			selected = append(selected, message)
		}
		break
	}
	return selected
}

func truncateHistoryMessageContent(
	prefix []conversationdomain.Message,
	message conversationdomain.Message,
	language ResponseLanguage,
	tokenBudget int,
) string {
	marker := historyTruncationMarker(language)
	runes := []rune(strings.TrimSpace(message.Content))
	low, high := 0, len(runes)
	best := ""
	for low <= high {
		middle := low + (high-low)/2
		content := strings.TrimSpace(string(runes[:middle]))
		if middle < len(runes) {
			content = strings.TrimSpace(content + marker)
		}
		candidateMessage := message
		candidateMessage.Content = content
		candidate := append(append([]conversationdomain.Message(nil), prefix...), candidateMessage)
		if content != "" && estimateTextTokens(formatConversationHistory(candidate, language)) <= tokenBudget {
			best = content
			low = middle + 1
		} else {
			high = middle - 1
		}
	}
	return best
}

// estimateTextTokens 是 Provider 无关的保守估算器。非 ASCII rune 近似按一个
// token 计算，ASCII 按三个字符一个 token，再统一增加 20% 安全余量。
// Provider 返回的真实 prompt_tokens 仍是最终观测事实。
func estimateTextTokens(value string) int {
	asciiRunes := 0
	nonASCIIRunes := 0
	for _, current := range value {
		if current <= 0x7f {
			asciiRunes++
		} else {
			nonASCIIRunes++
		}
	}
	base := nonASCIIRunes + (asciiRunes+2)/3
	if base == 0 {
		return 0
	}
	return (base*6 + 4) / 5
}

func historyTruncationMarker(language ResponseLanguage) string {
	if language == ResponseLanguageEnglish {
		return " [history truncated]"
	}
	return " [历史内容因预算截断]"
}

// buildContextualSearchQuery 用最近一条 completed 用户消息补足当前追问的检索语义。
// 它只产生一次 Embedding 请求，并始终完整保留当前问题；历史前缀按 Unicode
// rune 安全截断到 SemanticSearch 的既有 1000 字符上限。
func buildContextualSearchQuery(
	currentQuery string,
	history []conversationdomain.Message,
) string {
	currentQuery = strings.TrimSpace(currentQuery)
	if currentQuery == "" {
		return ""
	}
	previousUserMessage := ""
	for index := len(history) - 1; index >= 0; index-- {
		if history[index].Role == conversationdomain.RoleUser &&
			history[index].Status == conversationdomain.MessageStatusCompleted {
			previousUserMessage = strings.TrimSpace(history[index].Content)
			break
		}
	}
	if previousUserMessage == "" {
		return currentQuery
	}

	currentRunes := []rune(currentQuery)
	if len(currentRunes) >= embeddingapplication.MaxSemanticSearchQueryRunes {
		return string(currentRunes[:embeddingapplication.MaxSemanticSearchQueryRunes])
	}
	separator := "\n"
	remaining := embeddingapplication.MaxSemanticSearchQueryRunes - len(currentRunes) - len([]rune(separator))
	if remaining <= 0 {
		return currentQuery
	}
	previousRunes := []rune(previousUserMessage)
	if len(previousRunes) > remaining {
		previousRunes = previousRunes[:remaining]
	}
	return strings.TrimSpace(string(previousRunes)) + separator + currentQuery
}
