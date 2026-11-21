package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

type searchDocumentsPortFunc func(context.Context, accessdomain.OwnerScope, SearchDocumentsInput) ([]SearchDocumentsHit, error)

func (f searchDocumentsPortFunc) SearchDocuments(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	input SearchDocumentsInput,
) ([]SearchDocumentsHit, error) {
	return f(ctx, scope, input)
}

type readEvidencePortFunc func(context.Context, accessdomain.OwnerScope, []EvidenceLocator) ([]ReadEvidenceItem, error)

func (f readEvidencePortFunc) ReadDocumentEvidence(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	locators []EvidenceLocator,
) ([]ReadEvidenceItem, error) {
	return f(ctx, scope, locators)
}

func TestToolRegistryExposesOnlyFrozenDefinitionsAndStrictlyValidates(t *testing.T) {
	registry, err := NewToolRegistry(
		searchDocumentsPortFunc(func(context.Context, accessdomain.OwnerScope, SearchDocumentsInput) ([]SearchDocumentsHit, error) {
			return nil, nil
		}),
		readEvidencePortFunc(func(context.Context, accessdomain.OwnerScope, []EvidenceLocator) ([]ReadEvidenceItem, error) {
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	definitions := registry.Definitions()
	if len(definitions) != 2 || definitions[0].Name != ToolSearchDocuments || definitions[1].Name != ToolReadDocumentEvidence {
		t.Fatalf("tool definitions = %+v", definitions)
	}
	definitions[0].Parameters[0] = 'x'
	if registry.Definitions()[0].Parameters[0] == 'x' {
		t.Fatal("tool definition schema was not defensively copied")
	}
	documentID := int64(7)
	run := agentdomain.Run{ID: 1, RequestedDocumentID: &documentID}
	validSearch := ModelToolCall{
		Name:      ToolSearchDocuments,
		Arguments: json.RawMessage(`{"query":" bridge vibration ","strategy":"semantic","top_k":5}`),
	}
	canonical, err := registry.ValidateCall(run, nil, validSearch)
	if err != nil {
		t.Fatalf("ValidateCall(valid search) error = %v", err)
	}
	var normalized SearchDocumentsInput
	if err := json.Unmarshal(canonical, &normalized); err != nil || normalized.Query != "bridge vibration" ||
		normalized.DocumentID == nil || *normalized.DocumentID != documentID {
		t.Fatalf("normalized search = %+v, %v", normalized, err)
	}
	for name, call := range map[string]ModelToolCall{
		"unknown tool":     {Name: "write_document", Arguments: json.RawMessage(`{}`)},
		"unknown field":    {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x","strategy":"keyword","top_k":1,"sql":"x"}`)},
		"wrong key case":   {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"Query":"x","strategy":"keyword","top_k":1}`)},
		"duplicate key":    {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x","query":"y","strategy":"keyword","top_k":1}`)},
		"empty query":      {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":" ","strategy":"keyword","top_k":1}`)},
		"invalid strategy": {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x","strategy":"hybrid","top_k":1}`)},
		"top k":            {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x","strategy":"keyword","top_k":11}`)},
		"wrong document":   {Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x","strategy":"keyword","document_id":8,"top_k":1}`)},
		"bad ref syntax":   {Name: ToolReadDocumentEvidence, Arguments: json.RawMessage(`{"evidence_refs":["other-run"]}`)},
		"cross run ref":    {Name: ToolReadDocumentEvidence, Arguments: json.RawMessage(`{"evidence_refs":["step-9:item-1"]}`)},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := registry.ValidateCall(run, nil, call); err == nil {
				t.Fatalf("invalid call accepted: %+v", call)
			}
		})
	}
}

func TestToolRegistrySearchReadAndFinalSourcesStayInCurrentRun(t *testing.T) {
	scope, err := accessdomain.NewOwnerScope(42)
	if err != nil {
		t.Fatalf("NewOwnerScope() error = %v", err)
	}
	page := 3
	searchCalls := 0
	readCalls := 0
	registry, err := NewToolRegistry(
		searchDocumentsPortFunc(func(_ context.Context, gotScope accessdomain.OwnerScope, input SearchDocumentsInput) ([]SearchDocumentsHit, error) {
			searchCalls++
			if gotScope.OwnerUserID() != scope.OwnerUserID() || input.Query != "bridge" || input.TopK != 2 {
				t.Fatalf("search input = scope:%+v input:%+v", gotScope, input)
			}
			return []SearchDocumentsHit{{
				DocumentID: 7, ChunkID: 11, Title: "Paper", PageStart: &page, PageEnd: &page,
				Snippet: "bounded snippet", Score: 0.91,
			}}, nil
		}),
		readEvidencePortFunc(func(_ context.Context, gotScope accessdomain.OwnerScope, locators []EvidenceLocator) ([]ReadEvidenceItem, error) {
			readCalls++
			if gotScope.OwnerUserID() != scope.OwnerUserID() || len(locators) != 1 ||
				locators[0].EvidenceRef != "step-1:item-1" || locators[0].ChunkID != 11 {
				t.Fatalf("read input = scope:%+v locators:%+v", gotScope, locators)
			}
			return []ReadEvidenceItem{{
				DocumentID: 7, ChunkID: 11, Title: "Paper", PageStart: &page, PageEnd: &page,
				Excerpt: "bounded excerpt", ContentHash: "sha256:abc",
			}}, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	run := agentdomain.Run{ID: 99, OwnerUserID: scope.OwnerUserID()}
	searchName := ToolSearchDocuments
	searchStep := agentdomain.Step{
		ID: 1, RunID: run.ID, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusRunning, ToolName: &searchName,
		Arguments: json.RawMessage(`{"query":"bridge","strategy":"semantic","top_k":2}`),
	}
	wrongScope, _ := accessdomain.NewOwnerScope(scope.OwnerUserID() + 1)
	if _, err := registry.Execute(t.Context(), wrongScope, run, nil, searchStep); !errors.Is(err, ErrInvalidToolCall) {
		t.Fatalf("mismatched OwnerScope error = %v", err)
	}
	if searchCalls != 0 {
		t.Fatalf("mismatched OwnerScope reached search backend %d times", searchCalls)
	}
	searchResult, err := registry.Execute(t.Context(), scope, run, nil, searchStep)
	if err != nil {
		t.Fatalf("Execute(search) error = %v", err)
	}
	searchStep.Status = agentdomain.StepStatusSucceeded
	searchStep.Result = searchResult
	steps := []agentdomain.Step{searchStep}
	readCall := ModelToolCall{
		Name:      ToolReadDocumentEvidence,
		Arguments: json.RawMessage(`{"evidence_refs":["step-1:item-1"]}`),
	}
	canonicalRead, err := registry.ValidateCall(run, steps, readCall)
	if err != nil {
		t.Fatalf("ValidateCall(read) error = %v", err)
	}
	readName := ToolReadDocumentEvidence
	readStep := agentdomain.Step{
		ID: 2, RunID: run.ID, SequenceNo: 2, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusRunning, ToolName: &readName, Arguments: canonicalRead,
	}
	readResult, err := registry.Execute(t.Context(), scope, run, steps, readStep)
	if err != nil {
		t.Fatalf("Execute(read) error = %v", err)
	}
	readStep.Status = agentdomain.StepStatusSucceeded
	readStep.Result = readResult
	steps = append(steps, readStep)
	sources, err := registry.ResolveFinalSources(run, steps, []string{"step-1:item-1"})
	if err != nil || len(sources) != 1 || sources[0].DocumentID != 7 ||
		sources[0].ChunkID != 11 || sources[0].ContentHash != "sha256:abc" {
		t.Fatalf("ResolveFinalSources() = %+v, %v", sources, err)
	}
	if searchCalls != 1 || readCalls != 1 {
		t.Fatalf("tool calls = search:%d read:%d", searchCalls, readCalls)
	}
	otherRun := agentdomain.Run{ID: 100, OwnerUserID: scope.OwnerUserID()}
	if _, err := registry.ResolveFinalSources(otherRun, steps, []string{"step-1:item-1"}); !errors.Is(err, ErrEvidenceRefNotFound) {
		t.Fatalf("cross-run final source error = %v", err)
	}
	if _, err := registry.ResolveFinalSources(run, steps, nil); !errors.Is(err, ErrFinalEvidenceRequired) {
		t.Fatalf("empty final source error = %v", err)
	}
}

func TestToolRegistryRejectsOversizedEvidence(t *testing.T) {
	scope, _ := accessdomain.NewOwnerScope(1)
	registry, err := NewToolRegistry(
		searchDocumentsPortFunc(func(context.Context, accessdomain.OwnerScope, SearchDocumentsInput) ([]SearchDocumentsHit, error) {
			return []SearchDocumentsHit{{DocumentID: 1, ChunkID: 2, Snippet: strings.Repeat("x", maxSnippetRunes+1)}}, nil
		}),
		readEvidencePortFunc(func(context.Context, accessdomain.OwnerScope, []EvidenceLocator) ([]ReadEvidenceItem, error) {
			return nil, nil
		}),
	)
	if err != nil {
		t.Fatalf("NewToolRegistry() error = %v", err)
	}
	name := ToolSearchDocuments
	step := agentdomain.Step{
		ID: 1, RunID: 1, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
		Status: agentdomain.StepStatusRunning, ToolName: &name,
		Arguments: json.RawMessage(`{"query":"x","strategy":"keyword","top_k":1}`),
	}
	if _, err := registry.Execute(t.Context(), scope, agentdomain.Run{ID: 1, OwnerUserID: scope.OwnerUserID()}, nil, step); !errors.Is(err, ErrToolResultInvalid) {
		t.Fatalf("oversized search result error = %v", err)
	}
}

func TestToolRegistryRejectsPersistedEmptyEvidence(t *testing.T) {
	run := agentdomain.Run{ID: 1, OwnerUserID: 2}
	searchName := ToolSearchDocuments
	readName := ToolReadDocumentEvidence
	searchResult, _ := json.Marshal(searchDocumentsResult{Items: []searchEvidenceItem{{
		EvidenceRef: "step-1:item-1", DocumentID: 3, ChunkID: 4, Snippet: "snippet", Score: 1,
	}}})
	readResult, _ := json.Marshal(readDocumentEvidenceResult{Items: []readEvidenceResultItem{{
		EvidenceRef: "step-1:item-1", DocumentID: 3, ChunkID: 4, Excerpt: "   ", ContentHash: "hash",
	}}})
	steps := []agentdomain.Step{
		{
			ID: 1, RunID: run.ID, SequenceNo: 1, Kind: agentdomain.StepKindToolCall,
			Status: agentdomain.StepStatusSucceeded, ToolName: &searchName,
			Arguments: json.RawMessage(`{"query":"x","strategy":"keyword","top_k":1}`), Result: searchResult,
		},
		{
			ID: 2, RunID: run.ID, SequenceNo: 2, Kind: agentdomain.StepKindToolCall,
			Status: agentdomain.StepStatusSucceeded, ToolName: &readName,
			Arguments: json.RawMessage(`{"evidence_refs":["step-1:item-1"]}`), Result: readResult,
		},
	}
	registry, _ := NewToolRegistry(
		searchDocumentsPortFunc(func(context.Context, accessdomain.OwnerScope, SearchDocumentsInput) ([]SearchDocumentsHit, error) {
			return nil, nil
		}),
		readEvidencePortFunc(func(context.Context, accessdomain.OwnerScope, []EvidenceLocator) ([]ReadEvidenceItem, error) {
			return nil, nil
		}),
	)
	if _, err := registry.ResolveFinalSources(run, steps, []string{"step-1:item-1"}); !errors.Is(err, ErrToolResultInvalid) {
		t.Fatalf("empty persisted evidence error = %v", err)
	}
}
