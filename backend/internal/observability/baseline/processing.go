package baseline

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	// DefaultProcessingSlowThreshold 聚焦已知压力测试中超过常见 P95 的长尾任务。
	DefaultProcessingSlowThreshold = 60 * time.Second

	// DefaultProcessingSlowTaskLimit 限制报告中携带的明细数量，避免大日志生成巨型 JSON。
	DefaultProcessingSlowTaskLimit = 20
)

// Options 控制离线报告的慢任务分析口径。
type Options struct {
	ProcessingSlowThreshold time.Duration
	ProcessingSlowTaskLimit int
}

// DefaultOptions 返回命令行和库调用共同使用的默认分析口径。
func DefaultOptions() Options {
	return Options{
		ProcessingSlowThreshold: DefaultProcessingSlowThreshold,
		ProcessingSlowTaskLimit: DefaultProcessingSlowTaskLimit,
	}
}

func (o Options) validate() error {
	if o.ProcessingSlowThreshold <= 0 {
		return errors.New("processing slow threshold must be positive")
	}
	if o.ProcessingSlowTaskLimit <= 0 {
		return errors.New("processing slow task limit must be positive")
	}
	return nil
}

// ProcessingSummary 汇总文档解析任务的规模、阶段耗时和长尾瓶颈。
// EndToEndDuration 等于排队耗时加 Worker 执行耗时，更接近用户实际等待时间。
type ProcessingSummary struct {
	Events                  map[string]int       `json:"events"`
	Statuses                map[string]int       `json:"statuses"`
	ErrorCodes              map[string]int       `json:"error_codes"`
	SlowTaskBottlenecks     map[string]int       `json:"slow_task_bottlenecks"`
	FileBytesTotal          int64                `json:"file_bytes_total"`
	PageCountTotal          int                  `json:"page_count_total"`
	ChunkCountTotal         int                  `json:"chunk_count_total"`
	SlowTaskThresholdMS     int64                `json:"slow_task_threshold_ms"`
	SlowTaskCount           int                  `json:"slow_task_count"`
	SlowTaskSampleTruncated bool                 `json:"slow_task_sample_truncated"`
	SlowestTasks            []SlowProcessingTask `json:"slowest_tasks"`
	QueueWaitDuration       DurationSummary      `json:"queue_wait_duration"`
	ProcessorDuration       DurationSummary      `json:"processor_duration"`
	PythonDuration          DurationSummary      `json:"python_duration"`
	SourceOpenDuration      DurationSummary      `json:"source_open_duration"`
	MetadataReadDuration    DurationSummary      `json:"metadata_read_duration"`
	TextExtractDuration     DurationSummary      `json:"text_extract_duration"`
	TextSplitDuration       DurationSummary      `json:"text_split_duration"`
	ChunkWriteDuration      DurationSummary      `json:"chunk_write_duration"`
	FinalizationDuration    DurationSummary      `json:"finalization_duration"`
	WorkerDuration          DurationSummary      `json:"worker_duration"`
	EndToEndDuration        DurationSummary      `json:"end_to_end_duration"`
	SlowestPageDuration     DurationSummary      `json:"slowest_page_duration"`
}

// SlowProcessingTask 是可安全写入报告的慢任务摘要。
// 它故意不包含文件路径、标题或解析正文。
type SlowProcessingTask struct {
	ProcessingJobID int64  `json:"processing_job_id"`
	DocumentID      int64  `json:"document_id"`
	Status          string `json:"status"`
	ErrorCode       string `json:"error_code,omitempty"`
	Bottleneck      string `json:"bottleneck"`
	EndToEndMS      int64  `json:"end_to_end_ms"`
	QueueWaitMS     int64  `json:"queue_wait_ms"`
	WorkerMS        int64  `json:"worker_ms"`
	ProcessorMS     int64  `json:"processor_ms"`
	PythonTotalMS   *int64 `json:"python_total_ms,omitempty"`
	TextExtractMS   *int64 `json:"text_extract_ms,omitempty"`
	ChunkWriteMS    *int64 `json:"chunk_write_ms,omitempty"`
	FinalizeMS      *int64 `json:"finalize_ms,omitempty"`
	FileBytes       int64  `json:"file_bytes"`
	PageCount       *int   `json:"page_count,omitempty"`
	ChunkCount      int    `json:"chunk_count"`
	SlowestPage     *int   `json:"slowest_page_number,omitempty"`
	SlowestPageMS   *int64 `json:"slowest_page_ms,omitempty"`
}

type processingAccumulator struct {
	result       ProcessingSummary
	options      Options
	queueWait    durationAccumulator
	processor    durationAccumulator
	python       durationAccumulator
	sourceOpen   durationAccumulator
	metadataRead durationAccumulator
	textExtract  durationAccumulator
	textSplit    durationAccumulator
	chunkWrite   durationAccumulator
	finalization durationAccumulator
	worker       durationAccumulator
	endToEnd     durationAccumulator
	slowestPage  durationAccumulator
}

func newProcessingAccumulator(options Options) processingAccumulator {
	return processingAccumulator{
		options: options,
		result: ProcessingSummary{
			Events:              make(map[string]int),
			Statuses:            make(map[string]int),
			ErrorCodes:          make(map[string]int),
			SlowTaskBottlenecks: make(map[string]int),
			SlowTaskThresholdMS: options.ProcessingSlowThreshold.Milliseconds(),
			SlowestTasks:        make([]SlowProcessingTask, 0, options.ProcessingSlowTaskLimit),
		},
	}
}

func (a *processingAccumulator) aggregate(entry logEntry) error {
	if err := validateProcessingEntry(entry); err != nil {
		return err
	}

	a.result.Events[entry.Event]++
	a.result.Statuses[entry.Status]++
	addNonBlankMapValue(a.result.ErrorCodes, entry.ErrorCode)
	a.result.FileBytesTotal += *entry.FileBytes
	a.result.ChunkCountTotal += *entry.ChunkCount
	if entry.PageCount != nil {
		a.result.PageCountTotal += *entry.PageCount
	}

	endToEndMS := *entry.QueueWaitMS + *entry.TotalMS
	a.queueWait.add(*entry.QueueWaitMS)
	a.processor.add(*entry.ProcessorMS)
	a.worker.add(*entry.TotalMS)
	a.endToEnd.add(endToEndMS)
	a.addOptionalDurations(entry)

	if endToEndMS >= a.options.ProcessingSlowThreshold.Milliseconds() {
		bottleneck := processingBottleneck(entry)
		a.result.SlowTaskCount++
		a.result.SlowTaskBottlenecks[bottleneck]++
		a.addSlowTask(newSlowProcessingTask(entry, endToEndMS, bottleneck))
	}
	return nil
}

func (a *processingAccumulator) addOptionalDurations(entry logEntry) {
	addOptionalDuration(&a.python, entry.PythonTotalMS)
	addOptionalDuration(&a.sourceOpen, entry.SourceOpenMS)
	addOptionalDuration(&a.metadataRead, entry.MetadataReadMS)
	addOptionalDuration(&a.textExtract, entry.TextExtractMS)
	addOptionalDuration(&a.textSplit, entry.TextSplitMS)
	addOptionalDuration(&a.chunkWrite, entry.ChunkWriteMS)
	addOptionalDuration(&a.finalization, entry.FinalizeMS)
	addOptionalDuration(&a.slowestPage, entry.SlowestPageMS)
}

func (a *processingAccumulator) addSlowTask(task SlowProcessingTask) {
	a.result.SlowestTasks = append(a.result.SlowestTasks, task)
	sort.Slice(a.result.SlowestTasks, func(i, j int) bool {
		if a.result.SlowestTasks[i].EndToEndMS == a.result.SlowestTasks[j].EndToEndMS {
			return a.result.SlowestTasks[i].ProcessingJobID < a.result.SlowestTasks[j].ProcessingJobID
		}
		return a.result.SlowestTasks[i].EndToEndMS > a.result.SlowestTasks[j].EndToEndMS
	})
	if len(a.result.SlowestTasks) > a.options.ProcessingSlowTaskLimit {
		a.result.SlowestTasks = a.result.SlowestTasks[:a.options.ProcessingSlowTaskLimit]
	}
}

func (a processingAccumulator) summary() ProcessingSummary {
	a.result.SlowTaskSampleTruncated = a.result.SlowTaskCount > len(a.result.SlowestTasks)
	a.result.QueueWaitDuration = a.queueWait.summary()
	a.result.ProcessorDuration = a.processor.summary()
	a.result.PythonDuration = a.python.summary()
	a.result.SourceOpenDuration = a.sourceOpen.summary()
	a.result.MetadataReadDuration = a.metadataRead.summary()
	a.result.TextExtractDuration = a.textExtract.summary()
	a.result.TextSplitDuration = a.textSplit.summary()
	a.result.ChunkWriteDuration = a.chunkWrite.summary()
	a.result.FinalizationDuration = a.finalization.summary()
	a.result.WorkerDuration = a.worker.summary()
	a.result.EndToEndDuration = a.endToEnd.summary()
	a.result.SlowestPageDuration = a.slowestPage.summary()
	return a.result
}

func validateProcessingEntry(entry logEntry) error {
	if entry.ProcessingJobID == nil || *entry.ProcessingJobID <= 0 {
		return errors.New("processing event requires positive processing_job_id")
	}
	if entry.DocumentID == nil || *entry.DocumentID <= 0 {
		return errors.New("processing event requires positive document_id")
	}
	if strings.TrimSpace(entry.Status) == "" {
		return errors.New("processing event requires status")
	}

	requiredDurations := []struct {
		name  string
		value *int64
	}{
		{name: "queue_wait_ms", value: entry.QueueWaitMS},
		{name: "processor_ms", value: entry.ProcessorMS},
		{name: "total_ms", value: entry.TotalMS},
	}
	for _, field := range requiredDurations {
		if field.value == nil || *field.value < 0 {
			return fmt.Errorf("processing event requires non-negative %s", field.name)
		}
	}
	if entry.FileBytes == nil || *entry.FileBytes < 0 {
		return errors.New("processing event requires non-negative file_bytes")
	}
	if entry.ChunkCount == nil || *entry.ChunkCount < 0 {
		return errors.New("processing event requires non-negative chunk_count")
	}

	optionalDurations := []struct {
		name  string
		value *int64
	}{
		{name: "chunk_write_ms", value: entry.ChunkWriteMS},
		{name: "finalize_ms", value: entry.FinalizeMS},
		{name: "python_total_ms", value: entry.PythonTotalMS},
		{name: "source_open_ms", value: entry.SourceOpenMS},
		{name: "metadata_read_ms", value: entry.MetadataReadMS},
		{name: "text_extract_ms", value: entry.TextExtractMS},
		{name: "text_split_ms", value: entry.TextSplitMS},
		{name: "slowest_page_ms", value: entry.SlowestPageMS},
	}
	for _, field := range optionalDurations {
		if field.value != nil && *field.value < 0 {
			return fmt.Errorf("processing event requires non-negative %s", field.name)
		}
	}
	if entry.PageCount != nil && *entry.PageCount < 0 {
		return errors.New("processing event requires non-negative page_count")
	}
	if entry.SlowestPageNumber != nil && *entry.SlowestPageNumber < 0 {
		return errors.New("processing event requires non-negative slowest_page_number")
	}
	return nil
}

func addOptionalDuration(accumulator *durationAccumulator, value *int64) {
	if value != nil {
		accumulator.add(*value)
	}
}

func newSlowProcessingTask(
	entry logEntry,
	endToEndMS int64,
	bottleneck string,
) SlowProcessingTask {
	return SlowProcessingTask{
		ProcessingJobID: *entry.ProcessingJobID,
		DocumentID:      *entry.DocumentID,
		Status:          entry.Status,
		ErrorCode:       entry.ErrorCode,
		Bottleneck:      bottleneck,
		EndToEndMS:      endToEndMS,
		QueueWaitMS:     *entry.QueueWaitMS,
		WorkerMS:        *entry.TotalMS,
		ProcessorMS:     *entry.ProcessorMS,
		PythonTotalMS:   entry.PythonTotalMS,
		TextExtractMS:   entry.TextExtractMS,
		ChunkWriteMS:    entry.ChunkWriteMS,
		FinalizeMS:      entry.FinalizeMS,
		FileBytes:       *entry.FileBytes,
		PageCount:       entry.PageCount,
		ChunkCount:      *entry.ChunkCount,
		SlowestPage:     entry.SlowestPageNumber,
		SlowestPageMS:   entry.SlowestPageMS,
	}
}

type processingComponent struct {
	name string
	ms   int64
}

func processingBottleneck(entry logEntry) string {
	components := []processingComponent{{name: "queue_wait", ms: *entry.QueueWaitMS}}
	chunkWriteMS := optionalMilliseconds(entry.ChunkWriteMS)
	finalizeMS := optionalMilliseconds(entry.FinalizeMS)

	if entry.PythonTotalMS == nil {
		components = append(components, processingComponent{name: "processor", ms: *entry.ProcessorMS})
	} else {
		knownPythonMS := int64(0)
		pythonStages := []processingComponent{
			{name: "source_open", ms: optionalMilliseconds(entry.SourceOpenMS)},
			{name: "metadata_read", ms: optionalMilliseconds(entry.MetadataReadMS)},
			{name: "text_extract", ms: optionalMilliseconds(entry.TextExtractMS)},
			{name: "text_split", ms: optionalMilliseconds(entry.TextSplitMS)},
		}
		for _, stage := range pythonStages {
			knownPythonMS += stage.ms
			components = append(components, stage)
		}
		components = append(
			components,
			processingComponent{
				name: "python_other",
				ms:   max(*entry.PythonTotalMS-knownPythonMS, 0),
			},
			processingComponent{
				name: "processor_bridge_overhead",
				ms:   max(*entry.ProcessorMS-*entry.PythonTotalMS, 0),
			},
		)
	}

	components = append(
		components,
		processingComponent{name: "chunk_write", ms: chunkWriteMS},
		processingComponent{name: "finalization", ms: finalizeMS},
		processingComponent{
			name: "worker_other",
			ms:   max(*entry.TotalMS-*entry.ProcessorMS-chunkWriteMS-finalizeMS, 0),
		},
	)

	bottleneck := processingComponent{name: "unclassified"}
	for _, component := range components {
		if component.ms > bottleneck.ms {
			bottleneck = component
		}
	}
	return bottleneck.name
}

func optionalMilliseconds(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
