package answer

import (
	"context"
	"errors"
	"strings"
	"testing"

	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
	generationdomain "rag-reasoning-platform/backend/internal/domain/generation"
)

type fakeSemanticSearcher struct {
	searchFunc func(
		context.Context,
		embeddingapplication.SemanticSearchInput,
	) (embeddingapplication.SemanticSearchOutput, error)
}

func (f *fakeSemanticSearcher) Search(
	ctx context.Context,
	input embeddingapplication.SemanticSearchInput,
) (embeddingapplication.SemanticSearchOutput, error) {
	return f.searchFunc(ctx, input)
}

type fakeGenerator struct {
	generateFunc func(
		context.Context,
		generationdomain.GenerateRequest,
	) (generationdomain.GenerateResult, error)
}

func (f *fakeGenerator) Generate(
	ctx context.Context,
	request generationdomain.GenerateRequest,
) (generationdomain.GenerateResult, error) {
	return f.generateFunc(ctx, request)
}

func TestNewServiceValidatesDependenciesAndConfiguration(t *testing.T) {
	searcher := successfulSearcher(nil)
	generator := successfulGenerator()

	tests := []struct {
		name            string
		searcher        semanticSearcher
		generator       generationdomain.Generator
		modelName       string
		maxOutputTokens int
		temperature     float64
		wantedError     error
	}{
		{name: "missing searcher", generator: generator, modelName: "model", maxOutputTokens: 100, wantedError: ErrAnswerDependencies},
		{name: "missing generator", searcher: searcher, modelName: "model", maxOutputTokens: 100, wantedError: ErrAnswerDependencies},
		{name: "blank model", searcher: searcher, generator: generator, modelName: " ", maxOutputTokens: 100, wantedError: ErrAnswerConfiguration},
		{name: "invalid max output", searcher: searcher, generator: generator, modelName: "model", maxOutputTokens: 0, wantedError: ErrAnswerConfiguration},
		{name: "negative temperature", searcher: searcher, generator: generator, modelName: "model", maxOutputTokens: 100, temperature: -0.1, wantedError: ErrAnswerConfiguration},
		{name: "temperature above two", searcher: searcher, generator: generator, modelName: "model", maxOutputTokens: 100, temperature: 2.1, wantedError: ErrAnswerConfiguration},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, err := NewService(
				test.searcher,
				test.generator,
				test.modelName,
				test.maxOutputTokens,
				test.temperature,
			)
			if !errors.Is(err, test.wantedError) || service != nil {
				t.Fatalf("NewService() = (%v, %v), want (nil, %v)", service, err, test.wantedError)
			}
		})
	}
}

func TestServiceAnswerRetrievesEvidenceBeforeGeneration(t *testing.T) {
	documentID := int64(208)
	title := "Real-Time Malfunction Detection"
	page := 1
	hits := []documentdomain.SemanticSearchHit{
		{
			ChunkID:      501,
			DocumentID:   documentID,
			ChunkIndex:   0,
			Title:        &title,
			OriginalName: "mathematics-11-04045-v2.pdf",
			Content:      "Controllers are monitored in real time.",
			PageStart:    &page,
			PageEnd:      &page,
			Similarity:   0.87,
		},
	}

	callOrder := make([]string, 0, 2)
	searcher := &fakeSemanticSearcher{
		searchFunc: func(
			_ context.Context,
			input embeddingapplication.SemanticSearchInput,
		) (embeddingapplication.SemanticSearchOutput, error) {
			callOrder = append(callOrder, "search")
			if input.Query != "怎样检测故障？" ||
				input.DocumentID == nil || *input.DocumentID != documentID ||
				input.TopK != 5 {
				t.Fatalf("semantic input = %+v, want caller values", input)
			}
			return embeddingapplication.SemanticSearchOutput{
				Query: "怎样检测故障？",
				Hits:  hits,
			}, nil
		},
	}
	generator := &fakeGenerator{
		generateFunc: func(
			_ context.Context,
			request generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			callOrder = append(callOrder, "generate")
			if request.Model != "test-generation-model" ||
				request.MaxOutputTokens != 512 || request.Temperature != 0.1 {
				t.Fatalf("generation request = %+v, want configured values", request)
			}
			if !strings.Contains(request.SystemInstruction, "只能根据") ||
				!strings.Contains(request.SystemInstruction, "必须使用中文") ||
				!strings.Contains(request.UserPrompt, "证据 [1]") ||
				!strings.Contains(request.UserPrompt, hits[0].Content) {
				t.Fatalf("generation prompt does not contain grounded evidence: %+v", request)
			}
			return generationdomain.GenerateResult{
				Text:             "系统通过在线监测检测故障。[1]",
				PromptTokens:     100,
				CompletionTokens: 20,
				TotalTokens:      120,
			}, nil
		},
	}
	service := newServiceForTest(t, searcher, generator)

	result, err := service.Answer(context.Background(), Input{
		Query:      "怎样检测故障？",
		DocumentID: &documentID,
		TopK:       5,
	})
	if err != nil {
		t.Fatalf("Answer() error = %v, want nil", err)
	}
	if strings.Join(callOrder, ",") != "search,generate" {
		t.Fatalf("call order = %v, want search before generate", callOrder)
	}
	if result.Query != "怎样检测故障？" ||
		result.Answer != "系统通过在线监测检测故障。[1]" ||
		result.ResponseLanguage != ResponseLanguageChinese ||
		result.PromptTokens != 100 ||
		result.CompletionTokens != 20 || result.TotalTokens != 120 {
		t.Fatalf("output = %+v, want answer and token usage", result)
	}
	if len(result.Sources) != 1 ||
		result.Sources[0].Citation != 1 ||
		result.Sources[0].ChunkID != 501 ||
		result.Sources[0].DocumentID != documentID ||
		result.Sources[0].Similarity != 0.87 {
		t.Fatalf("sources = %+v, want numbered original evidence", result.Sources)
	}
}

func TestServiceAnswerDoesNotGenerateWithoutEvidence(t *testing.T) {
	generatorCalled := false
	searcher := successfulSearcher(make([]documentdomain.SemanticSearchHit, 0))
	generator := &fakeGenerator{
		generateFunc: func(
			context.Context,
			generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			generatorCalled = true
			return generationdomain.GenerateResult{}, nil
		},
	}
	service := newServiceForTest(t, searcher, generator)

	result, err := service.Answer(context.Background(), Input{
		Query:            "No evidence question",
		TopK:             5,
		ResponseLanguage: ResponseLanguageAuto,
	})
	if err != nil {
		t.Fatalf("Answer() error = %v, want nil", err)
	}
	if generatorCalled {
		t.Fatal("Generator was called without evidence")
	}
	if result.Answer != insufficientEvidenceAnswerEnglish ||
		result.ResponseLanguage != ResponseLanguageEnglish ||
		result.Sources == nil || len(result.Sources) != 0 ||
		result.TotalTokens != 0 {
		t.Fatalf("output = %+v, want safe zero-cost fallback", result)
	}
}

func TestServiceAnswerRejectsInvalidResponseLanguageBeforeDependencies(t *testing.T) {
	searchCalled := false
	generatorCalled := false
	searcher := &fakeSemanticSearcher{
		searchFunc: func(
			context.Context,
			embeddingapplication.SemanticSearchInput,
		) (embeddingapplication.SemanticSearchOutput, error) {
			searchCalled = true
			return embeddingapplication.SemanticSearchOutput{}, nil
		},
	}
	generator := &fakeGenerator{
		generateFunc: func(
			context.Context,
			generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			generatorCalled = true
			return generationdomain.GenerateResult{}, nil
		},
	}
	service := newServiceForTest(t, searcher, generator)

	_, err := service.Answer(context.Background(), Input{
		Query:            "question",
		TopK:             5,
		ResponseLanguage: ResponseLanguage("ja"),
	})
	if !errors.Is(err, ErrInvalidResponseLanguage) {
		t.Fatalf("Answer() error = %v, want %v", err, ErrInvalidResponseLanguage)
	}
	if searchCalled || generatorCalled {
		t.Fatalf(
			"dependencies called for invalid language: search=%t generate=%t",
			searchCalled,
			generatorCalled,
		)
	}
}

func TestServiceAnswerPreservesDependencyErrors(t *testing.T) {
	searchFailure := errors.New("search failed")
	generationFailure := errors.New("generation failed")
	oneHit := []documentdomain.SemanticSearchHit{{Content: "evidence"}}

	tests := []struct {
		name        string
		searcher    semanticSearcher
		generator   generationdomain.Generator
		wantedError error
	}{
		{
			name: "search failure",
			searcher: &fakeSemanticSearcher{
				searchFunc: func(
					context.Context,
					embeddingapplication.SemanticSearchInput,
				) (embeddingapplication.SemanticSearchOutput, error) {
					return embeddingapplication.SemanticSearchOutput{}, searchFailure
				},
			},
			generator:   successfulGenerator(),
			wantedError: searchFailure,
		},
		{
			name:     "generation failure",
			searcher: successfulSearcher(oneHit),
			generator: &fakeGenerator{
				generateFunc: func(
					context.Context,
					generationdomain.GenerateRequest,
				) (generationdomain.GenerateResult, error) {
					return generationdomain.GenerateResult{}, generationFailure
				},
			},
			wantedError: generationFailure,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service := newServiceForTest(t, test.searcher, test.generator)
			_, err := service.Answer(context.Background(), Input{
				Query: "question",
				TopK:  5,
			})
			if !errors.Is(err, test.wantedError) {
				t.Fatalf("Answer() error = %v, want wrapped %v", err, test.wantedError)
			}
		})
	}
}

func newServiceForTest(
	t *testing.T,
	searcher semanticSearcher,
	generator generationdomain.Generator,
) *Service {
	t.Helper()

	service, err := NewService(
		searcher,
		generator,
		"test-generation-model",
		512,
		0.1,
	)
	if err != nil {
		t.Fatalf("NewService() error = %v, want nil", err)
	}
	return service
}

func successfulSearcher(
	hits []documentdomain.SemanticSearchHit,
) *fakeSemanticSearcher {
	return &fakeSemanticSearcher{
		searchFunc: func(
			_ context.Context,
			input embeddingapplication.SemanticSearchInput,
		) (embeddingapplication.SemanticSearchOutput, error) {
			return embeddingapplication.SemanticSearchOutput{
				Query: strings.TrimSpace(input.Query),
				Hits:  hits,
			}, nil
		},
	}
}

func successfulGenerator() *fakeGenerator {
	return &fakeGenerator{
		generateFunc: func(
			context.Context,
			generationdomain.GenerateRequest,
		) (generationdomain.GenerateResult, error) {
			return generationdomain.GenerateResult{Text: "answer"}, nil
		},
	}
}
