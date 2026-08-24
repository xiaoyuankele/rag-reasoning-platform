package baseline

import (
	"strings"
	"testing"
	"time"
)

func TestSummarizeAggregatesEmbeddingAndGenerationEvents(t *testing.T) {
	input := strings.Join([]string{
		"[GIN-debug] GET /health",
		`{"event":"embedding_job_started","model_name":"text-embedding-v4","dimensions":1024}`,
		`{"event":"http_request_completed","duration_ms":25}`,
		`{"event":"embedding_job_succeeded","model_name":"text-embedding-v4","dimensions":1024,"attempt_count":2,"duration_ms":2000,"provider_duration_ms":1200,"finalization_duration_ms":100,"provider_call_count":2,"prompt_tokens":15,"total_tokens":20,"generated_vector_count":3,"retry_count":1,"recovered":true}`,
		`{"event":"embedding_job_requeued","model_name":"text-embedding-v4","dimensions":1024,"attempt_count":1,"duration_ms":1000,"provider_duration_ms":700,"finalization_duration_ms":50,"provider_call_count":1,"prompt_tokens":4,"total_tokens":5,"generated_vector_count":1,"retry_count":0,"recovered":false,"error_category":"provider_rate_limit"}`,
		`{"event":"answer_generation_started","model_name":"qwen3.6-flash","response_language":"zh","evidence_count":4}`,
		`{"event":"answer_generation_succeeded","model_name":"qwen3.6-flash","response_language":"zh","evidence_count":4,"provider_duration_ms":900,"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`,
		`{"event":"answer_generation_failed","model_name":"qwen3.6-flash","response_language":"en","evidence_count":3,"provider_duration_ms":400,"error_category":"provider_unavailable"}`,
		`{"event":"answer_generation_skipped","model_name":"qwen3.6-flash","response_language":"zh","evidence_count":0,"skip_reason":"insufficient_evidence"}`,
		`{"event":"answer_request_admitted","wait_duration_ms":10,"in_flight":1,"max_concurrency":2}`,
		`{"event":"answer_request_admitted","wait_duration_ms":20,"in_flight":2,"max_concurrency":2}`,
		`{"event":"answer_request_released","outcome":"succeeded","wait_duration_ms":10,"execution_duration_ms":1000,"in_flight":1,"max_concurrency":2}`,
		`{"event":"answer_request_rejected","outcome":"capacity_timeout","wait_duration_ms":3000,"in_flight":2,"max_concurrency":2}`,
		`{"event":"answer_request_released","outcome":"downstream_error","wait_duration_ms":20,"execution_duration_ms":400,"in_flight":0,"max_concurrency":2}`,
		`{"event":"answer_request_rejected","outcome":"canceled","wait_duration_ms":500,"in_flight":2,"max_concurrency":2}`,
	}, "\n")
	generatedAt := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60))

	report, err := Summarize(strings.NewReader(input), generatedAt)
	if err != nil {
		t.Fatalf("Summarize() error = %v, want nil", err)
	}

	if report.SchemaVersion != 4 ||
		report.GeneratedAt != "2026-08-15T04:00:00Z" ||
		report.ScannedLineCount != 15 ||
		report.JSONLineCount != 14 ||
		report.AggregatedEventCount != 11 ||
		report.IgnoredNonJSONLineCount != 1 ||
		report.IgnoredJSONEventCount != 3 {
		t.Fatalf("report metadata = %+v, want stable line counts", report)
	}

	embedding := report.Embedding
	if embedding.Events["embedding_job_succeeded"] != 1 ||
		embedding.Events["embedding_job_requeued"] != 1 ||
		embedding.Models["text-embedding-v4"] != 2 ||
		embedding.Dimensions["1024"] != 2 ||
		embedding.ErrorCategories["provider_rate_limit"] != 1 ||
		embedding.ProviderCallCount != 3 ||
		embedding.PromptTokens != 19 ||
		embedding.TotalTokens != 25 ||
		embedding.GeneratedVectorCount != 4 ||
		embedding.PersistedVectorCount != 3 ||
		embedding.RetriedJobCount != 1 ||
		embedding.RecoveredJobCount != 1 ||
		embedding.RetryExhaustedCount != 0 ||
		embedding.RecoveryRate != 1 {
		t.Fatalf("embedding summary = %+v, want accumulated attempts and cost", embedding)
	}
	assertDurationSummary(t, embedding.WorkerDuration, 2, 3000, 1500, 1000, 1000, 2000, 2000)
	assertDurationSummary(t, embedding.ProviderDuration, 2, 1900, 950, 700, 700, 1200, 1200)
	assertDurationSummary(t, embedding.FinalizationDuration, 2, 150, 75, 50, 50, 100, 100)

	generation := report.Generation
	if generation.Events["answer_generation_succeeded"] != 1 ||
		generation.Events["answer_generation_failed"] != 1 ||
		generation.Events["answer_generation_skipped"] != 1 ||
		generation.Models["qwen3.6-flash"] != 2 ||
		generation.ResponseLanguages["zh"] != 2 ||
		generation.ResponseLanguages["en"] != 1 ||
		generation.ErrorCategories["provider_unavailable"] != 1 ||
		generation.ProviderCallCount != 2 ||
		generation.PromptTokens != 100 ||
		generation.CompletionTokens != 20 ||
		generation.TotalTokens != 120 ||
		generation.EvidenceCountTotal != 7 ||
		generation.AverageEvidenceCount != float64(7)/3 {
		t.Fatalf("generation summary = %+v, want calls, skips and cost", generation)
	}
	assertDurationSummary(t, generation.ProviderDuration, 2, 1300, 650, 400, 400, 900, 900)

	admission := report.AnswerAdmission
	if admission.Events["answer_request_admitted"] != 2 ||
		admission.Events["answer_request_rejected"] != 2 ||
		admission.Events["answer_request_released"] != 2 ||
		admission.Outcomes["succeeded"] != 1 ||
		admission.Outcomes["downstream_error"] != 1 ||
		admission.Outcomes["capacity_timeout"] != 1 ||
		admission.Outcomes["canceled"] != 1 ||
		admission.CapacityTimeoutCount != 1 ||
		admission.CanceledWaitCount != 1 ||
		admission.MaxObservedInFlight != 2 {
		t.Fatalf("answer admission summary = %+v, want event and outcome counts", admission)
	}
	assertDurationSummary(t, admission.WaitDuration, 4, 3530, 882.5, 10, 20, 3000, 3000)
	assertDurationSummary(t, admission.ExecutionDuration, 2, 1400, 700, 400, 400, 1000, 1000)
}

func TestSummarizeTracksEmbeddingRetryExhaustion(t *testing.T) {
	input := `{"event":"embedding_job_failed","model_name":"text-embedding-v4","dimensions":1024,"attempt_count":3,"duration_ms":800,"provider_duration_ms":700,"finalization_duration_ms":20,"provider_call_count":1,"prompt_tokens":0,"total_tokens":0,"generated_vector_count":0,"retry_count":2,"recovered":false,"error_category":"timeout"}`

	report, err := Summarize(strings.NewReader(input), time.Time{})
	if err != nil {
		t.Fatalf("Summarize() error = %v, want nil", err)
	}

	embedding := report.Embedding
	if embedding.RetriedJobCount != 1 ||
		embedding.RecoveredJobCount != 0 ||
		embedding.RetryExhaustedCount != 1 ||
		embedding.RecoveryRate != 0 {
		t.Fatalf("embedding retry summary = %+v, want one exhausted retry", embedding)
	}
}

func TestSummarizeRejectsMalformedStructuredLog(t *testing.T) {
	_, err := Summarize(
		strings.NewReader("{not-json}"),
		time.Time{},
	)
	if err == nil || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("Summarize() error = %v, want malformed line error", err)
	}
}

func TestSummarizeRejectsIncompleteCostEvent(t *testing.T) {
	input := `{"event":"answer_generation_succeeded","model_name":"model","response_language":"zh","evidence_count":2}`
	_, err := Summarize(strings.NewReader(input), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "provider_duration_ms") {
		t.Fatalf("Summarize() error = %v, want missing duration error", err)
	}
}

func TestSummarizeRejectsInconsistentEmbeddingRecoveryFlag(t *testing.T) {
	input := `{"event":"embedding_job_succeeded","model_name":"model","dimensions":2,"attempt_count":1,"duration_ms":10,"provider_duration_ms":5,"provider_call_count":1,"prompt_tokens":1,"total_tokens":1,"generated_vector_count":1,"retry_count":0,"recovered":true}`

	_, err := Summarize(strings.NewReader(input), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "recovered flag") {
		t.Fatalf("Summarize() error = %v, want inconsistent recovery error", err)
	}
}

func TestSummarizeRejectsIncompleteAnswerAdmissionEvent(t *testing.T) {
	input := `{"event":"answer_request_released","outcome":"succeeded","wait_duration_ms":5,"in_flight":0,"max_concurrency":2}`
	_, err := Summarize(strings.NewReader(input), time.Time{})
	if err == nil || !strings.Contains(err.Error(), "execution_duration_ms") {
		t.Fatalf("Summarize() error = %v, want missing execution duration error", err)
	}
}

func TestSummarizeRequiresReader(t *testing.T) {
	_, err := Summarize(nil, time.Time{})
	if err == nil || err.Error() != "baseline log reader must be provided" {
		t.Fatalf("Summarize() error = %v, want missing reader error", err)
	}
}

func assertDurationSummary(
	t *testing.T,
	actual DurationSummary,
	count int,
	total int64,
	average float64,
	minimum int64,
	p50 int64,
	p95 int64,
	maximum int64,
) {
	t.Helper()
	if actual.Count != count || actual.TotalMS != total ||
		actual.AverageMS != average || actual.MinMS != minimum ||
		actual.P50MS != p50 || actual.P95MS != p95 || actual.MaxMS != maximum {
		t.Fatalf("duration summary = %+v, want count=%d total=%d average=%v min=%d p50=%d p95=%d max=%d", actual, count, total, average, minimum, p50, p95, maximum)
	}
}
