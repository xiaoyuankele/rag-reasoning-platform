// Package answer 编排“检索证据 → 生成回答”的 RAG 应用用例。
package answer

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

const (
	// InsufficientEvidenceAnswer 是没有任何检索结果时的稳定降级说明。
	// 此时不会调用远程生成模型，避免脱离知识库自由回答。
	InsufficientEvidenceAnswer = "现有文献中没有找到足够证据，暂时无法回答这个问题。"

	insufficientEvidenceAnswerEnglish = "The available documents do not contain enough evidence to answer this question."
)

var (
	// ErrAnswerDependencies 表示 Service 缺少检索器或生成器。
	ErrAnswerDependencies = errors.New(
		"answer service dependencies must be provided",
	)

	// ErrAnswerConfiguration 表示模型、最大输出或温度配置不合法。
	ErrAnswerConfiguration = errors.New(
		"answer service configuration is invalid",
	)
)

// semanticSearcher 是 AnswerService 实际需要的语义检索能力。
//
// 生产环境注入 *embedding.SemanticSearchService，测试可以注入 Fake。
type semanticSearcher interface {
	Search(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input embeddingapplication.SemanticSearchInput,
	) (embeddingapplication.SemanticSearchOutput, error)
}

// Input 是 Handler 等上层入口交给问答用例的数据。
type Input struct {
	Query            string
	DocumentID       *int64
	TopK             int
	ResponseLanguage ResponseLanguage
}

// Source 表示交给生成模型的一条编号证据及其来源。
// Citation 从 1 开始，与答案中的 [1]、[2] 标记对应。
type Source struct {
	Citation     int
	ChunkID      int64
	DocumentID   int64
	ChunkIndex   int
	Title        *string
	OriginalName string
	PageStart    *int
	PageEnd      *int
	Similarity   float64
}

// Output 是问答用例返回给上层入口的统一结果。
type Output struct {
	Query            string
	Answer           string
	ResponseLanguage ResponseLanguage
	Sources          []Source
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Service 编排语义检索、Prompt 构造与远程文本生成。
type Service struct {
	searcher        semanticSearcher
	generator       generationdomain.Generator
	events          GenerationEventObserver
	modelName       string
	maxOutputTokens int
	temperature     float64
}

// NewService 创建带来源问答应用服务。
func NewService(
	searcher semanticSearcher,
	generator generationdomain.Generator,
	events GenerationEventObserver,
	modelName string,
	maxOutputTokens int,
	temperature float64,
) (*Service, error) {
	if searcher == nil || generator == nil || events == nil {
		return nil, ErrAnswerDependencies
	}

	modelName = strings.TrimSpace(modelName)
	if modelName == "" || maxOutputTokens <= 0 ||
		temperature < 0 || temperature > 2 {
		return nil, ErrAnswerConfiguration
	}

	return &Service{
		searcher:        searcher,
		generator:       generator,
		events:          events,
		modelName:       modelName,
		maxOutputTokens: maxOutputTokens,
		temperature:     temperature,
	}, nil
}

// Answer 先检索证据，再根据证据生成回答。
func (s *Service) Answer(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input Input,
) (Output, error) {
	responseLanguage, err := resolveResponseLanguage(
		input.ResponseLanguage,
		input.Query,
	)
	if err != nil {
		return Output{}, err
	}

	candidateTopK := input.TopK

	if input.DocumentID == nil &&
		input.TopK > 0 &&
		input.TopK <= embeddingapplication.MaxSemanticSearchTopK {
		candidateTopK = embeddingapplication.MaxSemanticSearchTopK
	}

	searchResult, err := s.searcher.Search(
		ctx,
		scope,
		embeddingapplication.SemanticSearchInput{
			Query:      input.Query,
			DocumentID: input.DocumentID,
			TopK:       candidateTopK,
		},
	)

	if err != nil {
		return Output{}, fmt.Errorf("retrieve evidence for answer: %w", err)
	}

	selectedHits := selectAnswerEvidence(
		searchResult.Hits,
		input.TopK,
		input.DocumentID == nil,
	)

	sources := newSources(selectedHits)
	if len(selectedHits) == 0 {
		s.events.ObserveGenerationEvent(ctx, GenerationEvent{
			Type:             GenerationEventSkipped,
			ModelName:        s.modelName,
			ResponseLanguage: responseLanguage,
			DocumentID:       input.DocumentID,
			RequestedTopK:    input.TopK,
			EvidenceCount:    0,
			SkipReason:       GenerationSkipReasonInsufficientEvidence,
		})

		return Output{
			Query:            searchResult.Query,
			Answer:           insufficientEvidenceAnswer(responseLanguage),
			ResponseLanguage: responseLanguage,
			Sources:          sources,
		}, nil
	}

	userPrompt, err := buildUserPrompt(
		searchResult.Query,
		selectedHits,
		responseLanguage,
	)
	if err != nil {
		return Output{}, fmt.Errorf("build answer prompt: %w", err)
	}

	baseEvent := GenerationEvent{
		ModelName:        s.modelName,
		ResponseLanguage: responseLanguage,
		DocumentID:       input.DocumentID,
		RequestedTopK:    input.TopK,
		EvidenceCount:    len(selectedHits),
	}
	s.events.ObserveGenerationEvent(ctx, withGenerationEventType(
		baseEvent,
		GenerationEventStarted,
	))

	providerStartedAt := time.Now()
	generated, err := s.generator.Generate(
		ctx,
		generationdomain.GenerateRequest{
			SystemInstruction: buildSystemInstruction(responseLanguage),
			UserPrompt:        userPrompt,
			Model:             s.modelName,
			MaxOutputTokens:   s.maxOutputTokens,
			Temperature:       s.temperature,
		},
	)
	providerDuration := time.Since(providerStartedAt)
	if err != nil {
		failedEvent := withGenerationEventType(baseEvent, GenerationEventFailed)
		failedEvent.ProviderDuration = providerDuration
		failedEvent.ErrorCategory = classifyGenerationError(err)
		failedEvent.Err = err
		s.events.ObserveGenerationEvent(ctx, failedEvent)

		return Output{}, fmt.Errorf("generate evidence-based answer: %w", err)
	}

	succeededEvent := withGenerationEventType(baseEvent, GenerationEventSucceeded)
	succeededEvent.ProviderDuration = providerDuration
	succeededEvent.PromptTokens = generated.PromptTokens
	succeededEvent.CompletionTokens = generated.CompletionTokens
	succeededEvent.TotalTokens = generated.TotalTokens
	s.events.ObserveGenerationEvent(ctx, succeededEvent)

	return Output{
		Query:            searchResult.Query,
		Answer:           generated.Text,
		ResponseLanguage: responseLanguage,
		Sources:          sources,
		PromptTokens:     generated.PromptTokens,
		CompletionTokens: generated.CompletionTokens,
		TotalTokens:      generated.TotalTokens,
	}, nil
}

func withGenerationEventType(
	event GenerationEvent,
	eventType GenerationEventType,
) GenerationEvent {
	event.Type = eventType
	return event
}

func insufficientEvidenceAnswer(language ResponseLanguage) string {
	if language == ResponseLanguageEnglish {
		return insufficientEvidenceAnswerEnglish
	}
	return InsufficientEvidenceAnswer
}

func newSources(hits []documentdomain.SemanticSearchHit) []Source {
	sources := make([]Source, 0, len(hits))
	for index, hit := range hits {
		sources = append(sources, Source{
			Citation:     index + 1,
			ChunkID:      hit.ChunkID,
			DocumentID:   hit.DocumentID,
			ChunkIndex:   hit.ChunkIndex,
			Title:        hit.Title,
			OriginalName: hit.OriginalName,
			PageStart:    hit.PageStart,
			PageEnd:      hit.PageEnd,
			Similarity:   hit.Similarity,
		})
	}
	return sources
}
