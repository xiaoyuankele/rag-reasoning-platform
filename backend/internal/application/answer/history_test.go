package answer

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	conversationdomain "rag-reasoning-platform/backend/internal/domain/conversation"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

type fakeConversationHistoryReader struct {
	messages       []conversationdomain.Message
	err            error
	scope          accessdomain.OwnerScope
	conversationID int64
	userMessageID  int64
	limit          int
}

func TestNewServiceWithConversationHistoryValidatesReaderAndBudget(t *testing.T) {
	validConfig := ConversationHistoryConfig{MaxTurns: 6, MaxTokens: 1_000, InputTokenBudget: 5_000}
	if service, err := NewServiceWithConversationHistory(
		successfulSearcher(nil), successfulGenerator(), newRecordingGenerationEventObserver(),
		nil, validConfig, "model", 512, 0.1,
	); service != nil || !errors.Is(err, ErrAnswerConfiguration) {
		t.Fatalf("missing reader service/error = %v/%v", service, err)
	}
	if service, err := NewServiceWithConversationHistory(
		successfulSearcher(nil), successfulGenerator(), newRecordingGenerationEventObserver(),
		&fakeConversationHistoryReader{},
		ConversationHistoryConfig{MaxTurns: 6, MaxTokens: 3_000, InputTokenBudget: 4_000},
		"model", 512, 0.1,
	); service != nil || !errors.Is(err, ErrAnswerConfiguration) {
		t.Fatalf("invalid budget service/error = %v/%v", service, err)
	}
}

func (f *fakeConversationHistoryReader) ListCompletedMessagesBefore(
	_ context.Context,
	scope accessdomain.OwnerScope,
	conversationID int64,
	userMessageID int64,
	limit int,
) ([]conversationdomain.Message, error) {
	f.scope = scope
	f.conversationID = conversationID
	f.userMessageID = userMessageID
	f.limit = limit
	return append([]conversationdomain.Message(nil), f.messages...), f.err
}

func TestSelectConversationHistoryUsesNewestCompleteTurns(t *testing.T) {
	messages := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "u1", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "a1", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(3, conversationdomain.RoleUser, "u2", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(4, conversationdomain.RoleAssistant, "a2", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(5, conversationdomain.RoleUser, "u3", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(6, conversationdomain.RoleAssistant, "a3", conversationdomain.MessageStatusCompleted),
	}

	selected := selectConversationHistory(messages, ResponseLanguageEnglish, 2, 1_000)
	if selected.TurnCount != 2 || len(selected.Messages) != 4 || !selected.Truncated {
		t.Fatalf("selected history = %+v, want newest two of three turns", selected)
	}
	wanted := []string{"u2", "a2", "u3", "a3"}
	for index, content := range wanted {
		if selected.Messages[index].Content != content {
			t.Fatalf("messages[%d] = %q, want %q", index, selected.Messages[index].Content, content)
		}
	}
}

func TestExportedConversationHistorySelectorReusesM31Semantics(t *testing.T) {
	messages := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "old", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "answer", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(3, conversationdomain.RoleUser, strings.Repeat("最近历史", 100), conversationdomain.MessageStatusCompleted),
	}
	want := selectConversationHistory(messages, ResponseLanguageChinese, 1, 60)
	got, language, err := SelectConversationHistoryForQuestion(
		messages,
		ResponseLanguageAuto,
		"当前问题",
		1,
		60,
	)
	if err != nil || language != ResponseLanguageChinese || !reflect.DeepEqual(got, want) {
		t.Fatalf("exported selector = %+v/%s/%v, want %+v/zh", got, language, err, want)
	}
	config := ConversationHistoryConfig{MaxTurns: 6, MaxTokens: 1000, InputTokenBudget: 5000}
	if config.CandidateMessageLimit() != 14 {
		t.Fatalf("candidate limit = %d, want 14", config.CandidateMessageLimit())
	}
}

func TestSelectConversationHistoryKeepsStandaloneUserAndExcludesNonCompleted(t *testing.T) {
	messages := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "saved note", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "orphan assistant", conversationdomain.MessageStatusGenerating),
		testHistoryMessage(3, conversationdomain.RoleUser, "question", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(4, conversationdomain.RoleAssistant, "failed answer", conversationdomain.MessageStatusFailed),
	}

	selected := selectConversationHistory(messages, ResponseLanguageEnglish, 6, 1_000)
	if selected.TurnCount != 2 || len(selected.Messages) != 2 {
		t.Fatalf("selected history = %+v, want two standalone completed user turns", selected)
	}
	if selected.Messages[0].Content != "saved note" || selected.Messages[1].Content != "question" {
		t.Fatalf("selected messages = %+v", selected.Messages)
	}
}

func TestSelectConversationHistoryTruncatesOversizedLatestTurnSafely(t *testing.T) {
	messages := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, strings.Repeat("中文历史", 100), conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, strings.Repeat("回答内容", 100), conversationdomain.MessageStatusCompleted),
	}

	selected := selectConversationHistory(messages, ResponseLanguageChinese, 6, 80)
	if selected.TurnCount != 1 || len(selected.Messages) == 0 || !selected.Truncated {
		t.Fatalf("selected history = %+v, want a truncated latest turn", selected)
	}
	if selected.EstimatedTokens > 80 {
		t.Fatalf("estimated tokens = %d, want <= 80", selected.EstimatedTokens)
	}
	if !strings.Contains(selected.Messages[len(selected.Messages)-1].Content, "历史内容因预算截断") {
		t.Fatalf("truncated content = %q, want marker", selected.Messages[len(selected.Messages)-1].Content)
	}
}

func TestBuildBudgetedUserPromptIncludesHistoryAndDropsLowerEvidence(t *testing.T) {
	history := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "Which paper did we discuss?", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "We discussed bridge vibration.", conversationdomain.MessageStatusCompleted),
	}
	hits := []documentdomain.SemanticSearchHit{
		{ChunkID: 1, DocumentID: 1, OriginalName: "first.pdf", Content: strings.Repeat("first evidence ", 30)},
		{ChunkID: 2, DocumentID: 2, OriginalName: "second.pdf", Content: strings.Repeat("second evidence ", 80)},
	}
	onePrompt, err := buildUserPromptWithHistory("Why?", history, hits[:1], ResponseLanguageEnglish)
	if err != nil {
		t.Fatalf("build one-hit prompt: %v", err)
	}
	budget := estimateTextTokens(buildSystemInstructionWithHistory(ResponseLanguageEnglish, true)) +
		estimateTextTokens(onePrompt) + 5

	prompt, selected, estimated, err := buildBudgetedUserPrompt(
		"Why?",
		history,
		hits,
		ResponseLanguageEnglish,
		budget,
	)
	if err != nil {
		t.Fatalf("build budgeted prompt: %v", err)
	}
	if len(selected) != 1 || selected[0].ChunkID != 1 || estimated > budget {
		t.Fatalf("selected/estimate = %+v/%d, budget %d", selected, estimated, budget)
	}
	historyPosition := strings.Index(prompt, "Recent conversation history")
	questionPosition := strings.Index(prompt, "User question")
	evidencePosition := strings.Index(prompt, "Retrieved evidence")
	if historyPosition < 0 || historyPosition >= questionPosition || questionPosition >= evidencePosition {
		t.Fatalf("prompt section order is invalid:\n%s", prompt)
	}
	if strings.Contains(prompt, "second evidence") {
		t.Fatal("prompt contains lower-ranked evidence beyond input budget")
	}
}

func TestBuildBudgetedUserPromptTruncatesSingleOversizedEvidence(t *testing.T) {
	hit := documentdomain.SemanticSearchHit{
		ChunkID: 1, DocumentID: 2, OriginalName: "large.pdf",
		Content: strings.Repeat("超长证据内容", 2_000),
	}
	basePrompt, err := buildUserPrompt("当前问题", []documentdomain.SemanticSearchHit{{
		ChunkID: 1, DocumentID: 2, OriginalName: "large.pdf", Content: "短证据",
	}}, ResponseLanguageChinese)
	if err != nil {
		t.Fatalf("build base prompt: %v", err)
	}
	budget := estimateTextTokens(buildSystemInstruction(ResponseLanguageChinese)) +
		estimateTextTokens(basePrompt) + 20

	prompt, selected, estimated, err := buildBudgetedUserPrompt(
		"当前问题",
		nil,
		[]documentdomain.SemanticSearchHit{hit},
		ResponseLanguageChinese,
		budget,
	)
	if err != nil {
		t.Fatalf("build budgeted prompt: %v", err)
	}
	if len(selected) != 1 || estimated > budget ||
		!strings.Contains(prompt, "证据内容因输入预算截断") ||
		len(selected[0].Content) >= len(hit.Content) {
		t.Fatalf("truncated prompt/hit invalid: estimate=%d budget=%d selected=%+v", estimated, budget, selected)
	}
}

func TestBuildContextualSearchQueryUsesLatestUserAndKeepsCurrentQuestion(t *testing.T) {
	history := []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "old topic", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "old answer", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(3, conversationdomain.RoleUser, strings.Repeat("最近问题", 400), conversationdomain.MessageStatusCompleted),
		testHistoryMessage(4, conversationdomain.RoleAssistant, "recent answer", conversationdomain.MessageStatusCompleted),
	}
	current := "它使用了什么方法？"
	query := buildContextualSearchQuery(current, history)
	if !strings.HasSuffix(query, current) || strings.Contains(query, "old topic") ||
		len([]rune(query)) > embeddingapplication.MaxSemanticSearchQueryRunes {
		t.Fatalf("contextual search query is invalid: runes=%d query=%q", len([]rune(query)), query)
	}
	if got := buildContextualSearchQuery(current, nil); got != current {
		t.Fatalf("standalone contextual query = %q, want %q", got, current)
	}
}

func TestServiceAnswerLoadsFrozenConversationHistory(t *testing.T) {
	reader := &fakeConversationHistoryReader{messages: []conversationdomain.Message{
		testHistoryMessage(1, conversationdomain.RoleUser, "Tell me about paper A.", conversationdomain.MessageStatusCompleted),
		testHistoryMessage(2, conversationdomain.RoleAssistant, "Paper A studies bridges.", conversationdomain.MessageStatusCompleted),
	}}
	searcher := &fakeSemanticSearcher{searchFunc: func(
		_ context.Context,
		_ accessdomain.OwnerScope,
		input embeddingapplication.SemanticSearchInput,
	) (embeddingapplication.SemanticSearchOutput, error) {
		if !strings.Contains(input.Query, "Tell me about paper A.") ||
			!strings.HasSuffix(input.Query, "What method did it use?") {
			t.Fatalf("semantic search did not receive contextual query: %q", input.Query)
		}
		return embeddingapplication.SemanticSearchOutput{
			Query: input.Query,
			Hits: []documentdomain.SemanticSearchHit{{
				ChunkID: 1, DocumentID: 2, OriginalName: "paper-a.pdf", Content: "The experiment used sensors.",
			}},
		}, nil
	}}
	generator := &fakeGenerator{generateFunc: func(
		_ context.Context,
		request generationdomain.GenerateRequest,
	) (generationdomain.GenerateResult, error) {
		if !strings.Contains(request.UserPrompt, "Paper A studies bridges.") ||
			!strings.Contains(request.UserPrompt, "What method did it use?") ||
			!strings.Contains(request.SystemInstruction, "not factual evidence") {
			t.Fatalf("generation request does not contain bounded history: %+v", request)
		}
		return generationdomain.GenerateResult{Text: "It used sensors. [1]"}, nil
	}}
	observer := newRecordingGenerationEventObserver()
	service, err := NewServiceWithConversationHistory(
		searcher,
		generator,
		observer,
		reader,
		ConversationHistoryConfig{MaxTurns: 6, MaxTokens: 1_000, InputTokenBudget: 5_000},
		"test-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("create history-aware service: %v", err)
	}
	conversationID := int64(7)
	userMessageID := int64(9)
	if _, err := service.Answer(t.Context(), testAnswerOwnerScope(t), Input{
		Query:          "What method did it use?",
		TopK:           5,
		ConversationID: &conversationID,
		UserMessageID:  &userMessageID,
	}); err != nil {
		t.Fatalf("Answer() error = %v", err)
	}
	if reader.scope.OwnerUserID() != testAnswerOwnerUserID ||
		reader.conversationID != conversationID || reader.userMessageID != userMessageID ||
		reader.limit != 14 {
		t.Fatalf("history reader call = scope:%+v conversation:%d message:%d limit:%d", reader.scope, reader.conversationID, reader.userMessageID, reader.limit)
	}
	if len(observer.events) != 2 {
		t.Fatalf("generation events = %+v, want started and succeeded", observer.events)
	}
	started := observer.events[0]
	if started.HistoryMessageCount != 2 || started.HistoryTurnCount != 1 ||
		started.HistoryEstimatedTokens <= 0 || started.EstimatedInputTokens <= 0 ||
		started.HistoryTruncated {
		t.Fatalf("history generation event = %+v", started)
	}
}

func testHistoryMessage(
	sequence int64,
	role conversationdomain.Role,
	content string,
	status conversationdomain.MessageStatus,
) conversationdomain.Message {
	return conversationdomain.Message{
		ID:             sequence,
		ConversationID: 7,
		SequenceNo:     sequence,
		Role:           role,
		Content:        content,
		Status:         status,
	}
}
