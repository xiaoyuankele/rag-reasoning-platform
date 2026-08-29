package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestLoadApplicationRoleUsesAllByDefault(t *testing.T) {
	t.Setenv("APP_ROLE", "")

	role, err := LoadApplicationRole()
	if err != nil {
		t.Fatalf("LoadApplicationRole() error = %v, want nil", err)
	}
	if role != ApplicationRoleAll {
		t.Fatalf("LoadApplicationRole() = %q, want %q", role, ApplicationRoleAll)
	}
}

func TestLoadApplicationReadyFileUsesEmptyValueByDefault(t *testing.T) {
	t.Setenv("APP_READY_FILE", "")

	readyFile, err := LoadApplicationReadyFile()
	if err != nil {
		t.Fatalf("LoadApplicationReadyFile() error = %v, want nil", err)
	}
	if readyFile != "" {
		t.Fatalf("LoadApplicationReadyFile() = %q, want empty value", readyFile)
	}
}

func TestLoadApplicationReadyFileAcceptsAbsolutePath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "worker.ready")
	t.Setenv("APP_READY_FILE", want)

	readyFile, err := LoadApplicationReadyFile()
	if err != nil {
		t.Fatalf("LoadApplicationReadyFile() error = %v, want nil", err)
	}
	if readyFile != filepath.Clean(want) {
		t.Fatalf("LoadApplicationReadyFile() = %q, want %q", readyFile, want)
	}
}

func TestLoadApplicationReadyFileRejectsRelativePath(t *testing.T) {
	t.Setenv("APP_READY_FILE", "tmp/worker.ready")

	readyFile, err := LoadApplicationReadyFile()
	if !errors.Is(err, ErrApplicationReadyFileMustBeAbsolute) {
		t.Fatalf(
			"LoadApplicationReadyFile() error = %v, want ErrApplicationReadyFileMustBeAbsolute",
			err,
		)
	}
	if readyFile != "" {
		t.Fatalf("LoadApplicationReadyFile() = %q, want empty value", readyFile)
	}
}

func TestLoadApplicationRoleAcceptsEveryStableRole(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		want  ApplicationRole
	}{
		{name: "all", value: "all", want: ApplicationRoleAll},
		{name: "API normalized", value: " API ", want: ApplicationRoleAPI},
		{
			name:  "document worker",
			value: "document-worker",
			want:  ApplicationRoleDocumentWorker,
		},
		{
			name:  "embedding worker",
			value: "embedding-worker",
			want:  ApplicationRoleEmbeddingWorker,
		},
		{
			name:  "answer worker",
			value: "answer-worker",
			want:  ApplicationRoleAnswerWorker,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Setenv("APP_ROLE", testCase.value)

			role, err := LoadApplicationRole()
			if err != nil {
				t.Fatalf("LoadApplicationRole() error = %v, want nil", err)
			}
			if role != testCase.want {
				t.Fatalf(
					"LoadApplicationRole() = %q, want %q",
					role,
					testCase.want,
				)
			}
		})
	}
}

func TestLoadApplicationRoleRejectsUnsupportedValue(t *testing.T) {
	t.Setenv("APP_ROLE", "worker")

	role, err := LoadApplicationRole()
	if !errors.Is(err, ErrUnsupportedApplicationRole) {
		t.Fatalf(
			"LoadApplicationRole() error = %v, want ErrUnsupportedApplicationRole",
			err,
		)
	}
	if role != "" {
		t.Fatalf("LoadApplicationRole() role = %q, want empty value", role)
	}
}
