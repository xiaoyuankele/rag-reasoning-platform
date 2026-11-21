package main

import (
	"bytes"
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	loadtestuser "rag-reasoning-platform/backend/internal/maintenance/loadtestuser"
)

func TestRunDryRunDoesNotRequireDatabaseOrPassword(t *testing.T) {
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("LOAD_TEST_USER_PASSWORD", "")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := run(
		t.Context(),
		[]string{"-count", "110"},
		&stdout,
		&stderr,
	); err != nil {
		t.Fatalf("run() error = %v, stderr=%s", err, stderr.String())
	}
	output := stdout.String()
	for _, wanted := range []string{
		"DRY RUN",
		"account_count: 110",
		"loadtest-001@example.invalid",
		"loadtest-110@example.invalid",
	} {
		if !strings.Contains(output, wanted) {
			t.Errorf("dry-run output missing %q: %s", wanted, output)
		}
	}
}

func TestRunConfirmRequiresCountManifestAndPasswordBeforeDatabase(t *testing.T) {
	t.Setenv("DB_PASSWORD", "")
	t.Setenv("LOAD_TEST_USER_PASSWORD", "")
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			"expected count",
			[]string{"-confirm"},
			"-expected-count must equal",
		},
		{
			"manifest",
			[]string{"-confirm", "-expected-count", "110"},
			"-manifest is required",
		},
		{
			"password",
			[]string{
				"-confirm",
				"-expected-count",
				"110",
				"-manifest",
				filepath.Join(t.TempDir(), "users.csv"),
			},
			"LOAD_TEST_USER_PASSWORD must be provided",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			err := run(t.Context(), testCase.args, &stdout, &stderr)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf(
					"run() error = %v, want substring %q",
					err,
					testCase.want,
				)
			}
		})
	}
}

func TestWriteManifestContainsNoPasswordFieldsAndRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts", "manifest.csv")
	records := []loadtestuser.Record{{
		Index:       1,
		Email:       "loadtest-001@example.invalid",
		DisplayName: "Load Test User 001",
		Role:        loadtestuser.RoleBrowser,
		UserID:      17,
		Outcome:     loadtestuser.OutcomeCreated,
		CreatedAt:   time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC),
	}}
	if err := writeManifest(path, records); err != nil {
		t.Fatalf("writeManifest() error = %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 ||
		len(rows[0]) != 7 ||
		rows[1][1] != records[0].Email ||
		rows[1][3] != "17" {
		t.Fatalf("manifest rows = %+v", rows)
	}
	header := strings.Join(rows[0], ",")
	if strings.Contains(header, "password") ||
		strings.Contains(header, "cookie") ||
		strings.Contains(header, "session") {
		t.Fatalf("manifest has sensitive header: %s", header)
	}
	if err := writeManifest(path, records); err == nil ||
		!strings.Contains(err.Error(), "will not be overwritten") {
		t.Fatalf("second write error = %v, want overwrite refusal", err)
	}
}
