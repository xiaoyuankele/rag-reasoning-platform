// Package baseline 把后端 JSONL 结构化日志汇总成可重复比较的运行基线。
package baseline

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	answerapplication "rag-reasoning-platform/backend/internal/application/answer"
	embeddingapplication "rag-reasoning-platform/backend/internal/application/embedding"
)

const (
	// SchemaVersion 是基线报告结构版本；字段含义变化时必须递增。
	SchemaVersion       = 6
	maximumLogLineBytes = 1 << 20
)

// Report 是一次日志汇总的稳定 JSON 输出。
// Source 由调用者填写，通常是输入日志文件路径或 "stdin"。
type Report struct {
	SchemaVersion           int                    `json:"schema_version"`
	GeneratedAt             string                 `json:"generated_at"`
	Source                  string                 `json:"source"`
	ScannedLineCount        int                    `json:"scanned_line_count"`
	JSONLineCount           int                    `json:"json_line_count"`
	AggregatedEventCount    int                    `json:"aggregated_event_count"`
	IgnoredNonJSONLineCount int                    `json:"ignored_non_json_line_count"`
	IgnoredJSONEventCount   int                    `json:"ignored_json_event_count"`
	Embedding               EmbeddingSummary       `json:"embedding"`
	Generation              GenerationSummary      `json:"generation"`
	AnswerAdmission         AnswerAdmissionSummary `json:"answer_admission"`
	AnswerJobs              AnswerJobSummary       `json:"answer_jobs"`
	DocumentProcessing      ProcessingSummary      `json:"document_processing"`
}

// DurationSummary 使用毫秒描述一组耗时。
// P50/P95 采用 nearest-rank（向上取整）口径，便于小样本重复计算。
type DurationSummary struct {
	Count     int     `json:"count"`
	TotalMS   int64   `json:"total_ms"`
	AverageMS float64 `json:"average_ms"`
	MinMS     int64   `json:"min_ms"`
	P50MS     int64   `json:"p50_ms"`
	P95MS     int64   `json:"p95_ms"`
	MaxMS     int64   `json:"max_ms"`
}

// EmbeddingSummary 汇总所有已经结束一次 Worker 尝试的向量事件。
// GeneratedVectorCount 包含失败或重试前已经生成但未落库的向量；
// PersistedVectorCount 只统计 succeeded 事件中已经原子落库的向量。
type EmbeddingSummary struct {
	Events               map[string]int  `json:"events"`
	Models               map[string]int  `json:"models"`
	Dimensions           map[string]int  `json:"dimensions"`
	ErrorCategories      map[string]int  `json:"error_categories"`
	ProviderCallCount    int             `json:"provider_call_count"`
	PromptTokens         int             `json:"prompt_tokens"`
	TotalTokens          int             `json:"total_tokens"`
	GeneratedVectorCount int             `json:"generated_vector_count"`
	PersistedVectorCount int             `json:"persisted_vector_count"`
	RetriedJobCount      int             `json:"retried_job_count"`
	RecoveredJobCount    int             `json:"recovered_job_count"`
	RetryExhaustedCount  int             `json:"retry_exhausted_count"`
	RecoveryRate         float64         `json:"recovery_rate"`
	WorkerDuration       DurationSummary `json:"worker_duration"`
	ProviderDuration     DurationSummary `json:"provider_duration"`
	FinalizationDuration DurationSummary `json:"finalization_duration"`
}

// GenerationSummary 汇总在线问答的成功、失败和无证据跳过事件。
// ProviderCallCount 只包含 succeeded/failed；skipped 没有远程费用。
type GenerationSummary struct {
	Events               map[string]int  `json:"events"`
	Models               map[string]int  `json:"models"`
	ResponseLanguages    map[string]int  `json:"response_languages"`
	ErrorCategories      map[string]int  `json:"error_categories"`
	ProviderCallCount    int             `json:"provider_call_count"`
	PromptTokens         int             `json:"prompt_tokens"`
	CompletionTokens     int             `json:"completion_tokens"`
	TotalTokens          int             `json:"total_tokens"`
	EvidenceCountTotal   int             `json:"evidence_count_total"`
	AverageEvidenceCount float64         `json:"average_evidence_count"`
	ProviderDuration     DurationSummary `json:"provider_duration"`
}

// AnswerAdmissionSummary 汇总在线问答并发闸门的容量、等待和执行情况。
//
// Events 记录 admitted/rejected/released 生命周期事件；Outcomes 只记录
// released/rejected 终结事件。WaitDuration 也只使用终结事件，避免同一条
// 已准入请求在 admitted 和 released 中被重复统计。
type AnswerAdmissionSummary struct {
	Events               map[string]int  `json:"events"`
	Outcomes             map[string]int  `json:"outcomes"`
	CapacityTimeoutCount int             `json:"capacity_timeout_count"`
	OwnerCapacityCount   int             `json:"owner_capacity_count"`
	GlobalCapacityCount  int             `json:"global_capacity_count"`
	CanceledWaitCount    int             `json:"canceled_wait_count"`
	MaxObservedInFlight  int             `json:"max_observed_in_flight"`
	MaxObservedWaiting   int             `json:"max_observed_waiting"`
	MaxOwnerInFlight     int             `json:"max_owner_in_flight"`
	MaxOwnerWaiting      int             `json:"max_owner_waiting"`
	WaitDuration         DurationSummary `json:"wait_duration"`
	ExecutionDuration    DurationSummary `json:"execution_duration"`
}

type logEntry struct {
	Event                   string `json:"event"`
	AnswerJobID             *int64 `json:"answer_job_id"`
	ProcessingJobID         *int64 `json:"processing_job_id"`
	DocumentID              *int64 `json:"document_id"`
	Status                  string `json:"status"`
	ModelName               string `json:"model_name"`
	Dimensions              *int   `json:"dimensions"`
	ResponseLanguage        string `json:"response_language"`
	DurationMS              *int64 `json:"duration_ms"`
	ProviderDurationMS      *int64 `json:"provider_duration_ms"`
	ProviderCallCount       *int   `json:"provider_call_count"`
	PromptTokens            *int   `json:"prompt_tokens"`
	CompletionTokens        *int   `json:"completion_tokens"`
	TotalTokens             *int   `json:"total_tokens"`
	GeneratedVectorCount    *int   `json:"generated_vector_count"`
	AttemptCount            *int   `json:"attempt_count"`
	RetryCount              *int   `json:"retry_count"`
	Recovered               *bool  `json:"recovered"`
	FinalizationDuration    *int64 `json:"finalization_duration_ms"`
	EvidenceCount           *int   `json:"evidence_count"`
	ErrorCategory           string `json:"error_category"`
	Outcome                 string `json:"outcome"`
	WaitDurationMS          *int64 `json:"wait_duration_ms"`
	ExecutionDurationMS     *int64 `json:"execution_duration_ms"`
	InFlight                *int   `json:"in_flight"`
	MaxConcurrency          *int   `json:"max_concurrency"`
	OwnerInFlight           *int   `json:"owner_in_flight"`
	OwnerMaxConcurrency     *int   `json:"owner_max_concurrency"`
	Waiting                 *int   `json:"waiting"`
	MaxWaiting              *int   `json:"max_waiting"`
	OwnerWaiting            *int   `json:"owner_waiting"`
	OwnerMaxWaiting         *int   `json:"owner_max_waiting"`
	QueueWaitMS             *int64 `json:"queue_wait_ms"`
	ProcessorMS             *int64 `json:"processor_ms"`
	TotalMS                 *int64 `json:"total_ms"`
	FileBytes               *int64 `json:"file_bytes"`
	ChunkCount              *int   `json:"chunk_count"`
	ErrorCode               string `json:"error_code"`
	ChunkWriteMS            *int64 `json:"chunk_write_ms"`
	FinalizeMS              *int64 `json:"finalize_ms"`
	PythonTotalMS           *int64 `json:"python_total_ms"`
	SourceOpenMS            *int64 `json:"source_open_ms"`
	MetadataReadMS          *int64 `json:"metadata_read_ms"`
	TextExtractMS           *int64 `json:"text_extract_ms"`
	TextSplitMS             *int64 `json:"text_split_ms"`
	PageCount               *int   `json:"page_count"`
	SlowestPageNumber       *int   `json:"slowest_page_number"`
	SlowestPageMS           *int64 `json:"slowest_page_ms"`
	QueuedCount             *int64 `json:"queued_count"`
	ReadyQueuedCount        *int64 `json:"ready_queued_count"`
	ProcessingCount         *int64 `json:"processing_count"`
	MaxOwnerProcessingCount *int64 `json:"max_owner_processing_count"`
	OldestReadyWaitMS       *int64 `json:"oldest_ready_wait_ms"`
}

type durationAccumulator struct {
	values []int64
}

// Summarize 扫描混合了 Gin 文本与 slog JSON 的日志，并汇总模型终结事件与问答准入事件。
//
// 非 JSON 行会被计数后忽略；以 "{" 开头却无法解析的行会直接报错，避免悄悄丢失损坏的结构化日志。
// 模型 started 事件只表示过程开始，为防止重复计数不会进入最终成本统计。
func Summarize(reader io.Reader, generatedAt time.Time) (Report, error) {
	return SummarizeWithOptions(reader, generatedAt, DefaultOptions())
}

// SummarizeWithOptions 使用显式慢任务阈值生成报告。
// 它与 Summarize 一样只读取日志，不访问数据库或远程服务。
func SummarizeWithOptions(
	reader io.Reader,
	generatedAt time.Time,
	options Options,
) (Report, error) {
	if reader == nil {
		return Report{}, errors.New("baseline log reader must be provided")
	}
	if err := options.validate(); err != nil {
		return Report{}, err
	}

	report := newReport(generatedAt)
	processing := newProcessingAccumulator(options)
	var embeddingWorkerDurations durationAccumulator
	var embeddingProviderDurations durationAccumulator
	var embeddingFinalizationDurations durationAccumulator
	var generationProviderDurations durationAccumulator
	var answerWaitDurations durationAccumulator
	var answerExecutionDurations durationAccumulator
	answerJobs := newAnswerJobAccumulator()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maximumLogLineBytes)
	for scanner.Scan() {
		report.ScannedLineCount++
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "{") {
			report.IgnoredNonJSONLineCount++
			continue
		}

		var entry logEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return Report{}, fmt.Errorf(
				"decode structured log line %d: %w",
				report.ScannedLineCount,
				err,
			)
		}
		report.JSONLineCount++

		aggregated, err := aggregateEntry(
			&report,
			entry,
			&embeddingWorkerDurations,
			&embeddingProviderDurations,
			&embeddingFinalizationDurations,
			&generationProviderDurations,
			&answerWaitDurations,
			&answerExecutionDurations,
			&answerJobs,
			&processing,
		)
		if err != nil {
			return Report{}, fmt.Errorf(
				"aggregate structured log line %d: %w",
				report.ScannedLineCount,
				err,
			)
		}
		if aggregated {
			report.AggregatedEventCount++
		} else {
			report.IgnoredJSONEventCount++
		}
	}
	if err := scanner.Err(); err != nil {
		return Report{}, fmt.Errorf("scan structured logs: %w", err)
	}

	report.Embedding.WorkerDuration = embeddingWorkerDurations.summary()
	report.Embedding.ProviderDuration = embeddingProviderDurations.summary()
	report.Embedding.FinalizationDuration = embeddingFinalizationDurations.summary()
	if report.Embedding.RetriedJobCount > 0 {
		report.Embedding.RecoveryRate =
			float64(report.Embedding.RecoveredJobCount) / float64(report.Embedding.RetriedJobCount)
	}
	report.Generation.ProviderDuration = generationProviderDurations.summary()
	report.AnswerAdmission.WaitDuration = answerWaitDurations.summary()
	report.AnswerAdmission.ExecutionDuration = answerExecutionDurations.summary()
	report.AnswerJobs = answerJobs.result()
	report.DocumentProcessing = processing.summary()
	if generationEventCount := mapValueTotal(report.Generation.Events); generationEventCount > 0 {
		report.Generation.AverageEvidenceCount =
			float64(report.Generation.EvidenceCountTotal) / float64(generationEventCount)
	}
	return report, nil
}

func newReport(generatedAt time.Time) Report {
	return Report{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   generatedAt.UTC().Format(time.RFC3339Nano),
		Embedding: EmbeddingSummary{
			Events:          make(map[string]int),
			Models:          make(map[string]int),
			Dimensions:      make(map[string]int),
			ErrorCategories: make(map[string]int),
		},
		Generation: GenerationSummary{
			Events:            make(map[string]int),
			Models:            make(map[string]int),
			ResponseLanguages: make(map[string]int),
			ErrorCategories:   make(map[string]int),
		},
		AnswerAdmission: AnswerAdmissionSummary{
			Events:   make(map[string]int),
			Outcomes: make(map[string]int),
		},
		AnswerJobs: newAnswerJobAccumulator().result(),
	}
}

func aggregateEntry(
	report *Report,
	entry logEntry,
	embeddingWorkerDurations *durationAccumulator,
	embeddingProviderDurations *durationAccumulator,
	embeddingFinalizationDurations *durationAccumulator,
	generationProviderDurations *durationAccumulator,
	answerWaitDurations *durationAccumulator,
	answerExecutionDurations *durationAccumulator,
	answerJobs *answerJobAccumulator,
	processing *processingAccumulator,
) (bool, error) {
	switch entry.Event {
	case string(embeddingapplication.JobEventSucceeded),
		string(embeddingapplication.JobEventRequeued),
		string(embeddingapplication.JobEventFailed),
		string(embeddingapplication.JobEventInterrupted),
		string(embeddingapplication.JobEventUnfinished):
		return true, aggregateEmbeddingEntry(
			&report.Embedding,
			entry,
			embeddingWorkerDurations,
			embeddingProviderDurations,
			embeddingFinalizationDurations,
		)
	case string(answerapplication.GenerationEventSucceeded),
		string(answerapplication.GenerationEventFailed),
		string(answerapplication.GenerationEventSkipped):
		return true, aggregateGenerationEntry(
			&report.Generation,
			entry,
			generationProviderDurations,
		)
	case string(answerapplication.AnswerAdmissionEventAdmitted),
		string(answerapplication.AnswerAdmissionEventRejected),
		string(answerapplication.AnswerAdmissionEventReleased):
		return true, aggregateAnswerAdmissionEntry(
			&report.AnswerAdmission,
			entry,
			answerWaitDurations,
			answerExecutionDurations,
		)
	case string(answerapplication.JobEventSucceeded),
		string(answerapplication.JobEventRequeued),
		string(answerapplication.JobEventFailed),
		string(answerapplication.JobEventInterrupted),
		string(answerapplication.JobEventUnfinished):
		return true, answerJobs.aggregate(entry)
	case "processing_job_succeeded",
		"processing_job_failed",
		"processing_job_unfinished":
		return true, processing.aggregate(entry)
	default:
		return false, nil
	}
}

func aggregateAnswerAdmissionEntry(
	summary *AnswerAdmissionSummary,
	entry logEntry,
	waitDurations *durationAccumulator,
	executionDurations *durationAccumulator,
) error {
	if entry.WaitDurationMS == nil || *entry.WaitDurationMS < 0 {
		return errors.New("answer admission event requires non-negative wait_duration_ms")
	}
	if entry.InFlight == nil || *entry.InFlight < 0 {
		return errors.New("answer admission event requires non-negative in_flight")
	}
	if entry.MaxConcurrency == nil || *entry.MaxConcurrency <= 0 {
		return errors.New("answer admission event requires positive max_concurrency")
	}
	if *entry.InFlight > *entry.MaxConcurrency {
		return errors.New("answer admission in_flight must not exceed max_concurrency")
	}
	if err := validateOptionalAdmissionCapacity(
		entry.OwnerInFlight,
		entry.OwnerMaxConcurrency,
		"owner_in_flight",
		"owner_max_concurrency",
	); err != nil {
		return err
	}
	if err := validateOptionalAdmissionCapacity(
		entry.Waiting,
		entry.MaxWaiting,
		"waiting",
		"max_waiting",
	); err != nil {
		return err
	}
	if err := validateOptionalAdmissionCapacity(
		entry.OwnerWaiting,
		entry.OwnerMaxWaiting,
		"owner_waiting",
		"owner_max_waiting",
	); err != nil {
		return err
	}

	summary.Events[entry.Event]++
	if *entry.InFlight > summary.MaxObservedInFlight {
		summary.MaxObservedInFlight = *entry.InFlight
	}
	if entry.Waiting != nil && *entry.Waiting > summary.MaxObservedWaiting {
		summary.MaxObservedWaiting = *entry.Waiting
	}
	if entry.OwnerInFlight != nil && *entry.OwnerInFlight > summary.MaxOwnerInFlight {
		summary.MaxOwnerInFlight = *entry.OwnerInFlight
	}
	if entry.OwnerWaiting != nil && *entry.OwnerWaiting > summary.MaxOwnerWaiting {
		summary.MaxOwnerWaiting = *entry.OwnerWaiting
	}

	switch entry.Event {
	case string(answerapplication.AnswerAdmissionEventAdmitted):
		if *entry.InFlight == 0 {
			return errors.New("admitted answer event requires positive in_flight")
		}
		if strings.TrimSpace(entry.Outcome) != "" {
			return errors.New("admitted answer event must not have an outcome")
		}
		return nil

	case string(answerapplication.AnswerAdmissionEventRejected):
		if entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeCapacityTimeout) &&
			entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeOwnerCapacity) &&
			entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeGlobalCapacity) &&
			entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeCanceled) {
			return errors.New("rejected answer event has invalid outcome")
		}
		summary.Outcomes[entry.Outcome]++
		waitDurations.add(*entry.WaitDurationMS)
		switch entry.Outcome {
		case string(answerapplication.AnswerAdmissionOutcomeCapacityTimeout):
			summary.CapacityTimeoutCount++
		case string(answerapplication.AnswerAdmissionOutcomeOwnerCapacity):
			summary.OwnerCapacityCount++
		case string(answerapplication.AnswerAdmissionOutcomeGlobalCapacity):
			summary.GlobalCapacityCount++
		case string(answerapplication.AnswerAdmissionOutcomeCanceled):
			summary.CanceledWaitCount++
		}
		return nil

	case string(answerapplication.AnswerAdmissionEventReleased):
		if entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeSucceeded) &&
			entry.Outcome != string(answerapplication.AnswerAdmissionOutcomeDownstreamError) {
			return errors.New("released answer event has invalid outcome")
		}
		if entry.ExecutionDurationMS == nil || *entry.ExecutionDurationMS < 0 {
			return errors.New("released answer event requires non-negative execution_duration_ms")
		}
		summary.Outcomes[entry.Outcome]++
		waitDurations.add(*entry.WaitDurationMS)
		executionDurations.add(*entry.ExecutionDurationMS)
		return nil
	}

	return nil
}

// validateOptionalAdmissionCapacity 兼容旧日志：一组新字段可以同时缺省；
// 但只要出现当前值或上限中的任意一个，就必须成对且满足容量关系。
func validateOptionalAdmissionCapacity(
	value *int,
	maximum *int,
	valueName string,
	maximumName string,
) error {
	if value == nil && maximum == nil {
		return nil
	}
	if value == nil || maximum == nil {
		return fmt.Errorf(
			"answer admission %s and %s must be provided together",
			valueName,
			maximumName,
		)
	}
	if *value < 0 {
		return fmt.Errorf("answer admission %s must be non-negative", valueName)
	}
	if *maximum <= 0 {
		return fmt.Errorf("answer admission %s must be positive", maximumName)
	}
	if *value > *maximum {
		return fmt.Errorf(
			"answer admission %s must not exceed %s",
			valueName,
			maximumName,
		)
	}
	return nil
}

func aggregateEmbeddingEntry(
	summary *EmbeddingSummary,
	entry logEntry,
	workerDurations *durationAccumulator,
	providerDurations *durationAccumulator,
	finalizationDurations *durationAccumulator,
) error {
	if err := requireNonNegativeEmbeddingFields(entry); err != nil {
		return err
	}

	summary.Events[entry.Event]++
	addNonBlankMapValue(summary.Models, entry.ModelName)
	summary.Dimensions[fmt.Sprint(*entry.Dimensions)]++
	addNonBlankMapValue(summary.ErrorCategories, entry.ErrorCategory)
	summary.ProviderCallCount += *entry.ProviderCallCount
	summary.PromptTokens += *entry.PromptTokens
	summary.TotalTokens += *entry.TotalTokens
	summary.GeneratedVectorCount += *entry.GeneratedVectorCount
	if entry.Event == string(embeddingapplication.JobEventSucceeded) {
		summary.PersistedVectorCount += *entry.GeneratedVectorCount
	}
	retryCount := embeddingRetryCount(entry)
	recovered := entry.Event == string(embeddingapplication.JobEventSucceeded) && retryCount > 0
	if entry.Recovered != nil && *entry.Recovered != recovered {
		return errors.New("embedding recovered flag is inconsistent with event and retry_count")
	}
	if isTerminalEmbeddingEvent(entry.Event) && retryCount > 0 {
		summary.RetriedJobCount++
		if entry.Event == string(embeddingapplication.JobEventSucceeded) {
			summary.RecoveredJobCount++
		} else if entry.Event == string(embeddingapplication.JobEventFailed) {
			summary.RetryExhaustedCount++
		}
	}
	workerDurations.add(*entry.DurationMS)
	providerDurations.add(*entry.ProviderDurationMS)
	if entry.FinalizationDuration != nil {
		if *entry.FinalizationDuration < 0 {
			return errors.New("embedding event requires non-negative finalization_duration_ms")
		}
		finalizationDurations.add(*entry.FinalizationDuration)
	}
	return nil
}

func embeddingRetryCount(entry logEntry) int {
	if entry.RetryCount != nil {
		return *entry.RetryCount
	}
	if entry.AttemptCount != nil {
		return max(*entry.AttemptCount-1, 0)
	}
	return 0
}

func isTerminalEmbeddingEvent(event string) bool {
	return event == string(embeddingapplication.JobEventSucceeded) ||
		event == string(embeddingapplication.JobEventFailed)
}

func aggregateGenerationEntry(
	summary *GenerationSummary,
	entry logEntry,
	providerDurations *durationAccumulator,
) error {
	if strings.TrimSpace(entry.ModelName) == "" {
		return errors.New("generation event requires model_name")
	}
	if strings.TrimSpace(entry.ResponseLanguage) == "" {
		return errors.New("generation event requires response_language")
	}
	if entry.EvidenceCount == nil || *entry.EvidenceCount < 0 {
		return errors.New("generation event requires non-negative evidence_count")
	}

	summary.Events[entry.Event]++
	addNonBlankMapValue(summary.ResponseLanguages, entry.ResponseLanguage)
	addNonBlankMapValue(summary.ErrorCategories, entry.ErrorCategory)
	summary.EvidenceCountTotal += *entry.EvidenceCount

	if entry.Event == string(answerapplication.GenerationEventSkipped) {
		return nil
	}
	if entry.ProviderDurationMS == nil || *entry.ProviderDurationMS < 0 {
		return errors.New("generation provider event requires non-negative provider_duration_ms")
	}

	summary.ProviderCallCount++
	addNonBlankMapValue(summary.Models, entry.ModelName)
	providerDurations.add(*entry.ProviderDurationMS)
	if entry.Event == string(answerapplication.GenerationEventSucceeded) {
		if entry.PromptTokens == nil || *entry.PromptTokens < 0 ||
			entry.CompletionTokens == nil || *entry.CompletionTokens < 0 ||
			entry.TotalTokens == nil || *entry.TotalTokens < 0 {
			return errors.New("successful generation event requires non-negative token fields")
		}
		if *entry.PromptTokens+*entry.CompletionTokens > *entry.TotalTokens {
			return errors.New("generation total_tokens must cover prompt and completion tokens")
		}
		summary.PromptTokens += *entry.PromptTokens
		summary.CompletionTokens += *entry.CompletionTokens
		summary.TotalTokens += *entry.TotalTokens
	}
	return nil
}

func requireNonNegativeEmbeddingFields(entry logEntry) error {
	if strings.TrimSpace(entry.ModelName) == "" {
		return errors.New("embedding event requires model_name")
	}
	if entry.Dimensions == nil || *entry.Dimensions <= 0 {
		return errors.New("embedding event requires positive dimensions")
	}
	if entry.AttemptCount != nil && *entry.AttemptCount <= 0 {
		return errors.New("embedding event requires positive attempt_count when provided")
	}
	if entry.RetryCount != nil && *entry.RetryCount < 0 {
		return errors.New("embedding event requires non-negative retry_count when provided")
	}
	fields := []struct {
		name  string
		value *int
	}{
		{name: "provider_call_count", value: entry.ProviderCallCount},
		{name: "prompt_tokens", value: entry.PromptTokens},
		{name: "total_tokens", value: entry.TotalTokens},
		{name: "generated_vector_count", value: entry.GeneratedVectorCount},
	}
	for _, field := range fields {
		if field.value == nil || *field.value < 0 {
			return fmt.Errorf("embedding event requires non-negative %s", field.name)
		}
	}
	if *entry.PromptTokens > *entry.TotalTokens {
		return errors.New("embedding total_tokens must cover prompt_tokens")
	}
	if entry.DurationMS == nil || *entry.DurationMS < 0 {
		return errors.New("embedding event requires non-negative duration_ms")
	}
	if entry.ProviderDurationMS == nil || *entry.ProviderDurationMS < 0 {
		return errors.New("embedding event requires non-negative provider_duration_ms")
	}
	return nil
}

func addNonBlankMapValue(values map[string]int, value string) {
	value = strings.TrimSpace(value)
	if value != "" {
		values[value]++
	}
}

func mapValueTotal(values map[string]int) int {
	var total int
	for _, value := range values {
		total += value
	}
	return total
}

func (a *durationAccumulator) add(value int64) {
	a.values = append(a.values, value)
}

func (a durationAccumulator) summary() DurationSummary {
	if len(a.values) == 0 {
		return DurationSummary{}
	}

	values := append([]int64(nil), a.values...)
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	var total int64
	for _, value := range values {
		total += value
	}

	return DurationSummary{
		Count:     len(values),
		TotalMS:   total,
		AverageMS: float64(total) / float64(len(values)),
		MinMS:     values[0],
		P50MS:     nearestRank(values, 0.50),
		P95MS:     nearestRank(values, 0.95),
		MaxMS:     values[len(values)-1],
	}
}

func nearestRank(sortedValues []int64, percentile float64) int64 {
	rank := int(math.Ceil(percentile*float64(len(sortedValues)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sortedValues) {
		rank = len(sortedValues) - 1
	}
	return sortedValues[rank]
}
