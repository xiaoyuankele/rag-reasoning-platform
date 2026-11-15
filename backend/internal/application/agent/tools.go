package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"regexp"
	"strings"
	"unicode/utf8"

	accessdomain "rag-reasoning-platform/backend/internal/domain/access"
	agentdomain "rag-reasoning-platform/backend/internal/domain/agent"
)

const (
	ToolSearchDocuments      = "search_documents"
	ToolReadDocumentEvidence = "read_document_evidence"

	maxToolArgumentsBytes = 8 * 1024
	maxToolResultBytes    = 32 * 1024
	maxSearchQueryRunes   = 1000
	maxSearchTopK         = 10
	maxEvidenceRefs       = 10
	maxEvidenceTitleRunes = 500
	maxSnippetRunes       = 2000
	maxExcerptRunes       = 4000
)

var (
	ErrToolDependencies           = errors.New("agent tool dependencies must be provided")
	ErrInvalidToolCall            = errors.New("agent tool call is invalid")
	ErrEvidenceRefNotFound        = errors.New("agent evidence ref is unavailable")
	ErrFinalEvidenceRequired      = errors.New("agent final evidence is required")
	ErrToolTemporarilyUnavailable = errors.New("agent tool is temporarily unavailable")
	ErrToolResultInvalid          = errors.New("agent tool result is invalid")
)

type ToolDefinition struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

type SearchStrategy string

const (
	SearchStrategyKeyword  SearchStrategy = "keyword"
	SearchStrategySemantic SearchStrategy = "semantic"
)

type SearchDocumentsInput struct {
	Query      string         `json:"query"`
	Strategy   SearchStrategy `json:"strategy"`
	DocumentID *int64         `json:"document_id,omitempty"`
	TopK       int            `json:"top_k"`
}

type SearchDocumentsHit struct {
	DocumentID int64
	ChunkID    int64
	Title      string
	PageStart  *int
	PageEnd    *int
	Snippet    string
	Score      float64
}

type SearchDocumentsPort interface {
	// Implementations must constrain candidates by OwnerScope and ready status.
	SearchDocuments(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		input SearchDocumentsInput,
	) ([]SearchDocumentsHit, error)
}

type EvidenceLocator struct {
	EvidenceRef string
	DocumentID  int64
	ChunkID     int64
	Title       string
	PageStart   *int
	PageEnd     *int
}

type ReadEvidenceItem struct {
	DocumentID  int64
	ChunkID     int64
	Title       string
	PageStart   *int
	PageEnd     *int
	Excerpt     string
	ContentHash string
}

// ReadDocumentEvidencePort must revalidate OwnerScope, ready status and chunk
// ownership for every trusted locator before returning bounded excerpts.
type ReadDocumentEvidencePort interface {
	ReadDocumentEvidence(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		locators []EvidenceLocator,
	) ([]ReadEvidenceItem, error)
}

type searchEvidenceItem struct {
	EvidenceRef string  `json:"evidence_ref"`
	DocumentID  int64   `json:"document_id"`
	ChunkID     int64   `json:"chunk_id"`
	Title       string  `json:"title,omitempty"`
	PageStart   *int    `json:"page_start,omitempty"`
	PageEnd     *int    `json:"page_end,omitempty"`
	Snippet     string  `json:"snippet"`
	Score       float64 `json:"score"`
}

type searchDocumentsResult struct {
	Items []searchEvidenceItem `json:"items"`
}

type readEvidenceResultItem struct {
	EvidenceRef string `json:"evidence_ref"`
	DocumentID  int64  `json:"document_id"`
	ChunkID     int64  `json:"chunk_id"`
	Title       string `json:"title,omitempty"`
	PageStart   *int   `json:"page_start,omitempty"`
	PageEnd     *int   `json:"page_end,omitempty"`
	Excerpt     string `json:"excerpt"`
	ContentHash string `json:"content_hash"`
}

type readDocumentEvidenceResult struct {
	Items []readEvidenceResultItem `json:"items"`
}

type readEvidenceArguments struct {
	EvidenceRefs []string `json:"evidence_refs"`
}

type FinalSource struct {
	EvidenceRef string `json:"evidence_ref"`
	DocumentID  int64  `json:"document_id"`
	ChunkID     int64  `json:"chunk_id"`
	Title       string `json:"title,omitempty"`
	PageStart   *int   `json:"page_start,omitempty"`
	PageEnd     *int   `json:"page_end,omitempty"`
	ContentHash string `json:"content_hash"`
}

// ToolSet separates validation from execution so a Worker can persist the
// normalized decision before invoking a Tool.
type ToolSet interface {
	Definitions() []ToolDefinition
	ValidateCall(run agentdomain.Run, steps []agentdomain.Step, call ModelToolCall) (json.RawMessage, error)
	Execute(
		ctx context.Context,
		scope accessdomain.OwnerScope,
		run agentdomain.Run,
		steps []agentdomain.Step,
		step agentdomain.Step,
	) (json.RawMessage, error)
	ResolveFinalSources(
		run agentdomain.Run,
		steps []agentdomain.Step,
		evidenceRefs []string,
	) ([]FinalSource, error)
}

type ToolRegistry struct {
	search SearchDocumentsPort
	read   ReadDocumentEvidencePort
}

func NewToolRegistry(search SearchDocumentsPort, read ReadDocumentEvidencePort) (*ToolRegistry, error) {
	if search == nil || read == nil {
		return nil, ErrToolDependencies
	}
	return &ToolRegistry{search: search, read: read}, nil
}

var searchDocumentsSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["query","strategy","top_k"],
  "properties":{
    "query":{"type":"string","minLength":1,"maxLength":1000},
    "strategy":{"type":"string","enum":["keyword","semantic"]},
    "document_id":{"type":"integer","minimum":1},
    "top_k":{"type":"integer","minimum":1,"maximum":10}
  }
}`)

var readDocumentEvidenceSchema = json.RawMessage(`{
  "type":"object",
  "additionalProperties":false,
  "required":["evidence_refs"],
  "properties":{
    "evidence_refs":{"type":"array","minItems":1,"maxItems":10,"uniqueItems":true,"items":{"type":"string"}}
  }
}`)

func (r *ToolRegistry) Definitions() []ToolDefinition {
	return []ToolDefinition{
		{
			Name:        ToolSearchDocuments,
			Description: "Search owner-scoped ready document chunks and return compact evidence references.",
			Parameters:  append(json.RawMessage(nil), searchDocumentsSchema...),
		},
		{
			Name:        ToolReadDocumentEvidence,
			Description: "Read bounded excerpts for evidence references returned by this Run.",
			Parameters:  append(json.RawMessage(nil), readDocumentEvidenceSchema...),
		},
	}
}

func (r *ToolRegistry) ValidateCall(
	run agentdomain.Run,
	steps []agentdomain.Step,
	call ModelToolCall,
) (json.RawMessage, error) {
	if run.ID <= 0 || len(call.Arguments) == 0 || len(call.Arguments) > maxToolArgumentsBytes {
		return nil, ErrInvalidToolCall
	}
	switch call.Name {
	case ToolSearchDocuments:
		input, err := decodeSearchDocumentsInput(call.Arguments)
		if err != nil {
			return nil, err
		}
		if run.RequestedDocumentID != nil {
			if input.DocumentID != nil && *input.DocumentID != *run.RequestedDocumentID {
				return nil, ErrInvalidToolCall
			}
			documentID := *run.RequestedDocumentID
			input.DocumentID = &documentID
		}
		return marshalBounded(input, maxToolArgumentsBytes, ErrInvalidToolCall)
	case ToolReadDocumentEvidence:
		input, err := decodeReadEvidenceArguments(call.Arguments)
		if err != nil {
			return nil, err
		}
		ledger, err := buildSearchEvidenceLedger(run, steps)
		if err != nil {
			return nil, err
		}
		for _, ref := range input.EvidenceRefs {
			if _, exists := ledger[ref]; !exists {
				return nil, ErrEvidenceRefNotFound
			}
		}
		return marshalBounded(input, maxToolArgumentsBytes, ErrInvalidToolCall)
	default:
		return nil, ErrInvalidToolCall
	}
}

func (r *ToolRegistry) Execute(
	ctx context.Context,
	scope accessdomain.OwnerScope,
	run agentdomain.Run,
	steps []agentdomain.Step,
	step agentdomain.Step,
) (json.RawMessage, error) {
	if !scope.IsValid() || run.OwnerUserID <= 0 || scope.OwnerUserID() != run.OwnerUserID ||
		step.RunID != run.ID || step.Kind != agentdomain.StepKindToolCall ||
		step.Status != agentdomain.StepStatusRunning || step.ToolName == nil {
		return nil, ErrInvalidToolCall
	}
	canonical, err := r.ValidateCall(run, steps, ModelToolCall{Name: *step.ToolName, Arguments: step.Arguments})
	if err != nil {
		return nil, err
	}
	switch *step.ToolName {
	case ToolSearchDocuments:
		input, _ := decodeSearchDocumentsInput(canonical)
		hits, err := r.search.SearchDocuments(ctx, scope, input)
		if err != nil {
			return nil, err
		}
		if len(hits) > input.TopK {
			hits = hits[:input.TopK]
		}
		items := make([]searchEvidenceItem, 0, len(hits))
		for index, hit := range hits {
			if !validSearchHit(hit) {
				return nil, ErrToolResultInvalid
			}
			items = append(items, searchEvidenceItem{
				EvidenceRef: evidenceRef(step.SequenceNo, index+1),
				DocumentID:  hit.DocumentID,
				ChunkID:     hit.ChunkID,
				Title:       hit.Title,
				PageStart:   hit.PageStart,
				PageEnd:     hit.PageEnd,
				Snippet:     hit.Snippet,
				Score:       hit.Score,
			})
		}
		return marshalBounded(searchDocumentsResult{Items: items}, maxToolResultBytes, ErrToolResultInvalid)
	case ToolReadDocumentEvidence:
		input, _ := decodeReadEvidenceArguments(canonical)
		ledger, err := buildSearchEvidenceLedger(run, steps)
		if err != nil {
			return nil, err
		}
		locators := make([]EvidenceLocator, 0, len(input.EvidenceRefs))
		for _, ref := range input.EvidenceRefs {
			locators = append(locators, ledger[ref])
		}
		readItems, err := r.read.ReadDocumentEvidence(ctx, scope, locators)
		if err != nil {
			return nil, err
		}
		if len(readItems) != len(locators) {
			return nil, ErrToolResultInvalid
		}
		items := make([]readEvidenceResultItem, 0, len(readItems))
		for index, item := range readItems {
			locator := locators[index]
			if !validReadEvidence(item, locator) {
				return nil, ErrToolResultInvalid
			}
			items = append(items, readEvidenceResultItem{
				EvidenceRef: locator.EvidenceRef,
				DocumentID:  item.DocumentID,
				ChunkID:     item.ChunkID,
				Title:       locator.Title,
				PageStart:   locator.PageStart,
				PageEnd:     locator.PageEnd,
				Excerpt:     item.Excerpt,
				ContentHash: item.ContentHash,
			})
		}
		return marshalBounded(readDocumentEvidenceResult{Items: items}, maxToolResultBytes, ErrToolResultInvalid)
	default:
		return nil, ErrInvalidToolCall
	}
}

func (r *ToolRegistry) ResolveFinalSources(
	run agentdomain.Run,
	steps []agentdomain.Step,
	evidenceRefs []string,
) ([]FinalSource, error) {
	if len(evidenceRefs) == 0 || len(evidenceRefs) > maxEvidenceRefs {
		return nil, ErrFinalEvidenceRequired
	}
	ledger, err := buildReadEvidenceLedger(run, steps)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(evidenceRefs))
	sources := make([]FinalSource, 0, len(evidenceRefs))
	for _, ref := range evidenceRefs {
		if strings.TrimSpace(ref) != ref || len(ref) > 64 || !evidenceRefPattern.MatchString(ref) {
			return nil, ErrEvidenceRefNotFound
		}
		if _, duplicate := seen[ref]; duplicate {
			return nil, ErrInvalidToolCall
		}
		item, exists := ledger[ref]
		if !exists {
			return nil, ErrEvidenceRefNotFound
		}
		seen[ref] = struct{}{}
		sources = append(sources, FinalSource{
			EvidenceRef: item.EvidenceRef,
			DocumentID:  item.DocumentID,
			ChunkID:     item.ChunkID,
			Title:       item.Title,
			PageStart:   item.PageStart,
			PageEnd:     item.PageEnd,
			ContentHash: item.ContentHash,
		})
	}
	return sources, nil
}

func decodeSearchDocumentsInput(raw json.RawMessage) (SearchDocumentsInput, error) {
	var input SearchDocumentsInput
	if err := validateRootObjectKeys(raw, "query", "strategy", "document_id", "top_k"); err != nil {
		return SearchDocumentsInput{}, ErrInvalidToolCall
	}
	if err := decodeStrictJSON(raw, &input); err != nil {
		return SearchDocumentsInput{}, ErrInvalidToolCall
	}
	input.Query = strings.TrimSpace(input.Query)
	if input.Query == "" || !utf8.ValidString(input.Query) || utf8.RuneCountInString(input.Query) > maxSearchQueryRunes ||
		(input.Strategy != SearchStrategyKeyword && input.Strategy != SearchStrategySemantic) ||
		(input.DocumentID != nil && *input.DocumentID <= 0) || input.TopK <= 0 || input.TopK > maxSearchTopK {
		return SearchDocumentsInput{}, ErrInvalidToolCall
	}
	return input, nil
}

func decodeReadEvidenceArguments(raw json.RawMessage) (readEvidenceArguments, error) {
	var input readEvidenceArguments
	if err := validateRootObjectKeys(raw, "evidence_refs"); err != nil {
		return readEvidenceArguments{}, ErrInvalidToolCall
	}
	if err := decodeStrictJSON(raw, &input); err != nil || len(input.EvidenceRefs) == 0 || len(input.EvidenceRefs) > maxEvidenceRefs {
		return readEvidenceArguments{}, ErrInvalidToolCall
	}
	seen := make(map[string]struct{}, len(input.EvidenceRefs))
	for _, ref := range input.EvidenceRefs {
		if strings.TrimSpace(ref) != ref || len(ref) > 64 || !evidenceRefPattern.MatchString(ref) {
			return readEvidenceArguments{}, ErrInvalidToolCall
		}
		if _, duplicate := seen[ref]; duplicate {
			return readEvidenceArguments{}, ErrInvalidToolCall
		}
		seen[ref] = struct{}{}
	}
	return input, nil
}

func decodeStrictJSON(raw json.RawMessage, output any) error {
	if len(raw) == 0 || len(raw) > maxToolArgumentsBytes || !json.Valid(raw) {
		return ErrInvalidToolCall
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalidToolCall
	}
	return nil
}

func validateRootObjectKeys(raw json.RawMessage, allowedKeys ...string) error {
	if len(raw) == 0 || len(raw) > maxToolArgumentsBytes || !json.Valid(raw) {
		return ErrInvalidToolCall
	}
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[key] = struct{}{}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return ErrInvalidToolCall
	}
	seen := make(map[string]struct{}, len(allowedKeys))
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ErrInvalidToolCall
		}
		key, ok := token.(string)
		if !ok {
			return ErrInvalidToolCall
		}
		if _, exists := allowed[key]; !exists {
			return ErrInvalidToolCall
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrInvalidToolCall
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return ErrInvalidToolCall
		}
	}
	closing, err := decoder.Token()
	if err != nil || closing != json.Delim('}') {
		return ErrInvalidToolCall
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) || token != nil {
		return ErrInvalidToolCall
	}
	return nil
}

func buildSearchEvidenceLedger(run agentdomain.Run, steps []agentdomain.Step) (map[string]EvidenceLocator, error) {
	ledger := make(map[string]EvidenceLocator)
	for _, step := range steps {
		if step.RunID != run.ID {
			return nil, ErrEvidenceRefNotFound
		}
		if step.Status != agentdomain.StepStatusSucceeded || step.Kind != agentdomain.StepKindToolCall ||
			step.ToolName == nil || *step.ToolName != ToolSearchDocuments {
			continue
		}
		var result searchDocumentsResult
		if err := json.Unmarshal(step.Result, &result); err != nil {
			return nil, ErrToolResultInvalid
		}
		for index, item := range result.Items {
			expectedRef := evidenceRef(step.SequenceNo, index+1)
			if item.EvidenceRef != expectedRef || !validSearchHit(SearchDocumentsHit{
				DocumentID: item.DocumentID, ChunkID: item.ChunkID, Title: item.Title,
				PageStart: item.PageStart, PageEnd: item.PageEnd, Snippet: item.Snippet, Score: item.Score,
			}) {
				return nil, ErrToolResultInvalid
			}
			if _, duplicate := ledger[item.EvidenceRef]; duplicate {
				return nil, ErrToolResultInvalid
			}
			ledger[item.EvidenceRef] = EvidenceLocator{
				EvidenceRef: item.EvidenceRef,
				DocumentID:  item.DocumentID,
				ChunkID:     item.ChunkID,
				Title:       item.Title,
				PageStart:   item.PageStart,
				PageEnd:     item.PageEnd,
			}
		}
	}
	return ledger, nil
}

func buildReadEvidenceLedger(run agentdomain.Run, steps []agentdomain.Step) (map[string]readEvidenceResultItem, error) {
	searchLedger, err := buildSearchEvidenceLedger(run, steps)
	if err != nil {
		return nil, err
	}
	ledger := make(map[string]readEvidenceResultItem)
	for _, step := range steps {
		if step.RunID != run.ID {
			return nil, ErrEvidenceRefNotFound
		}
		if step.Status != agentdomain.StepStatusSucceeded || step.Kind != agentdomain.StepKindToolCall ||
			step.ToolName == nil || *step.ToolName != ToolReadDocumentEvidence {
			continue
		}
		var result readDocumentEvidenceResult
		if err := json.Unmarshal(step.Result, &result); err != nil {
			return nil, ErrToolResultInvalid
		}
		arguments, err := decodeReadEvidenceArguments(step.Arguments)
		if err != nil || len(arguments.EvidenceRefs) != len(result.Items) {
			return nil, ErrToolResultInvalid
		}
		for index, item := range result.Items {
			if item.EvidenceRef != arguments.EvidenceRefs[index] {
				return nil, ErrToolResultInvalid
			}
			searchItem, exists := searchLedger[item.EvidenceRef]
			if !exists || !validReadEvidence(ReadEvidenceItem{
				DocumentID: item.DocumentID, ChunkID: item.ChunkID, Title: item.Title,
				PageStart: item.PageStart, PageEnd: item.PageEnd, Excerpt: item.Excerpt, ContentHash: item.ContentHash,
			}, searchItem) {
				return nil, ErrToolResultInvalid
			}
			if previous, duplicate := ledger[item.EvidenceRef]; duplicate {
				if !reflect.DeepEqual(previous, item) {
					return nil, ErrToolResultInvalid
				}
				continue
			}
			ledger[item.EvidenceRef] = item
		}
	}
	return ledger, nil
}

func validSearchHit(hit SearchDocumentsHit) bool {
	return hit.DocumentID > 0 && hit.ChunkID > 0 && validEvidenceTitle(hit.Title) &&
		strings.TrimSpace(hit.Snippet) != "" && utf8.ValidString(hit.Snippet) &&
		utf8.RuneCountInString(hit.Snippet) <= maxSnippetRunes &&
		!math.IsNaN(hit.Score) && !math.IsInf(hit.Score, 0) && validPageRange(hit.PageStart, hit.PageEnd)
}

func validReadEvidence(item ReadEvidenceItem, locator EvidenceLocator) bool {
	return item.DocumentID == locator.DocumentID && item.ChunkID == locator.ChunkID &&
		item.Title == locator.Title && optionalIntEqual(item.PageStart, locator.PageStart) &&
		optionalIntEqual(item.PageEnd, locator.PageEnd) && validEvidenceTitle(item.Title) &&
		strings.TrimSpace(item.Excerpt) != "" &&
		utf8.ValidString(item.Excerpt) && utf8.RuneCountInString(item.Excerpt) <= maxExcerptRunes &&
		strings.TrimSpace(item.ContentHash) == item.ContentHash && item.ContentHash != "" && len(item.ContentHash) <= 128 &&
		validPageRange(item.PageStart, item.PageEnd)
}

func validEvidenceTitle(title string) bool {
	return utf8.ValidString(title) && utf8.RuneCountInString(title) <= maxEvidenceTitleRunes
}

func validPageRange(start, end *int) bool {
	if start == nil || end == nil {
		return start == nil && end == nil
	}
	return *start > 0 && *end >= *start
}

func evidenceRef(sequenceNo, itemNo int) string {
	return fmt.Sprintf("step-%d:item-%d", sequenceNo, itemNo)
}

var evidenceRefPattern = regexp.MustCompile(`^step-[1-9][0-9]*:item-[1-9][0-9]*$`)

func optionalIntEqual(left, right *int) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func marshalBounded(value any, limit int, stableErr error) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > limit {
		return nil, stableErr
	}
	return raw, nil
}
