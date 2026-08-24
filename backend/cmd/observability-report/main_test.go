package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rag-reasoning-platform/backend/internal/observability/baseline"
)

func TestRunReadsLogAndWritesReport(t *testing.T) {
	temporaryDirectory := t.TempDir()
	inputPath := filepath.Join(temporaryDirectory, "server.jsonl")
	outputPath := filepath.Join(temporaryDirectory, "report.json")
	input := `{"event":"answer_generation_succeeded","model_name":"model","response_language":"zh","evidence_count":2,"provider_duration_ms":300,"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}`
	if err := os.WriteFile(inputPath, []byte(input), 0o600); err != nil {
		t.Fatalf("write input log: %v", err)
	}

	if err := run(inputPath, outputPath); err != nil {
		t.Fatalf("run() error = %v, want nil", err)
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read output report: %v", err)
	}
	var report baseline.Report
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatalf("decode output report: %v", err)
	}
	if report.Source != inputPath ||
		report.SchemaVersion != 5 ||
		report.Generation.ProviderCallCount != 1 ||
		report.Generation.TotalTokens != 12 ||
		report.AnswerAdmission.Events == nil ||
		report.AnswerAdmission.Outcomes == nil ||
		report.DocumentProcessing.Events == nil ||
		report.DocumentProcessing.SlowTaskThresholdMS != 60000 {
		t.Fatalf("report = %+v, want source and generation totals", report)
	}
}
