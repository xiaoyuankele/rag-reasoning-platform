package baseline

import (
	"strings"
	"testing"
	"time"
)

func TestSummarizeClassifiesSlowDocumentProcessingBottlenecks(t *testing.T) {
	input := strings.Join([]string{
		`{"event":"processing_job_succeeded","processing_job_id":101,"document_id":201,"status":"succeeded","queue_wait_ms":10000,"processor_ms":70000,"total_ms":80000,"file_bytes":10485760,"chunk_count":120,"chunk_write_ms":5000,"finalize_ms":1000,"python_total_ms":60000,"source_open_ms":1000,"metadata_read_ms":1000,"text_extract_ms":55000,"text_split_ms":2000,"page_count":100,"slowest_page_number":42,"slowest_page_ms":3000}`,
		`{"event":"processing_job_failed","processing_job_id":102,"document_id":202,"status":"failed","queue_wait_ms":50000,"processor_ms":10000,"total_ms":15000,"file_bytes":5242880,"chunk_count":0,"finalize_ms":2000,"error_code":"processor_timeout"}`,
	}, "\n")
	options := Options{
		ProcessingSlowThreshold: 60 * time.Second,
		ProcessingSlowTaskLimit: 1,
	}

	report, err := SummarizeWithOptions(strings.NewReader(input), time.Time{}, options)
	if err != nil {
		t.Fatalf("SummarizeWithOptions() error = %v, want nil", err)
	}

	processing := report.DocumentProcessing
	if processing.Events["processing_job_succeeded"] != 1 ||
		processing.Events["processing_job_failed"] != 1 ||
		processing.Statuses["succeeded"] != 1 ||
		processing.Statuses["failed"] != 1 ||
		processing.ErrorCodes["processor_timeout"] != 1 ||
		processing.FileBytesTotal != 15728640 ||
		processing.PageCountTotal != 100 ||
		processing.ChunkCountTotal != 120 {
		t.Fatalf("processing totals = %+v, want two accumulated jobs", processing)
	}
	if processing.SlowTaskCount != 2 ||
		!processing.SlowTaskSampleTruncated ||
		processing.SlowTaskBottlenecks["text_extract"] != 1 ||
		processing.SlowTaskBottlenecks["queue_wait"] != 1 ||
		len(processing.SlowestTasks) != 1 {
		t.Fatalf("slow task summary = %+v, want two tasks and one retained detail", processing)
	}
	slowest := processing.SlowestTasks[0]
	if slowest.ProcessingJobID != 101 ||
		slowest.DocumentID != 201 ||
		slowest.EndToEndMS != 90000 ||
		slowest.Bottleneck != "text_extract" ||
		slowest.SlowestPage == nil || *slowest.SlowestPage != 42 {
		t.Fatalf("slowest task = %+v, want text extraction bottleneck", slowest)
	}

	assertDurationSummary(t, processing.QueueWaitDuration, 2, 60000, 30000, 10000, 10000, 50000, 50000)
	assertDurationSummary(t, processing.ProcessorDuration, 2, 80000, 40000, 10000, 10000, 70000, 70000)
	assertDurationSummary(t, processing.EndToEndDuration, 2, 155000, 77500, 65000, 65000, 90000, 90000)
	assertDurationSummary(t, processing.TextExtractDuration, 1, 55000, 55000, 55000, 55000, 55000, 55000)
}

func TestSummarizeWithOptionsRejectsInvalidProcessingAnalysisOptions(t *testing.T) {
	testCases := []Options{
		{ProcessingSlowThreshold: 0, ProcessingSlowTaskLimit: 1},
		{ProcessingSlowThreshold: time.Second, ProcessingSlowTaskLimit: 0},
	}
	for _, options := range testCases {
		if _, err := SummarizeWithOptions(strings.NewReader(""), time.Time{}, options); err == nil {
			t.Fatalf("SummarizeWithOptions(%+v) error = nil, want validation error", options)
		}
	}
}

func TestSummarizeRejectsIncompleteProcessingEvent(t *testing.T) {
	input := `{"event":"processing_job_succeeded","processing_job_id":1,"document_id":2,"status":"succeeded"}`

	_, err := Summarize(strings.NewReader(input), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "queue_wait_ms") {
		t.Fatalf("Summarize() error = %v, want missing queue wait error", err)
	}
}
