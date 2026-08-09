package database

import (
	"testing"
	"testing/fstest"
)

func TestReadMigrationNormalizesLineEndings(t *testing.T) {
	tests := []struct {
		name    string
		content string
	}{
		{name: "LF", content: "SELECT 1;\nSELECT 2;\n"},
		{name: "CRLF", content: "SELECT 1;\r\nSELECT 2;\r\n"},
		{name: "CR", content: "SELECT 1;\rSELECT 2;\r"},
	}

	var expectedChecksum string
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			loaded, err := readMigration(
				fstest.MapFS{
					"000001_line_endings.up.sql": {
						Data: []byte(test.content),
					},
				},
				"000001_line_endings.up.sql",
			)
			if err != nil {
				t.Fatalf("readMigration() error = %v, want nil", err)
			}

			if loaded.sql != "SELECT 1;\nSELECT 2;\n" {
				t.Fatalf(
					"readMigration() SQL = %q, want normalized LF SQL",
					loaded.sql,
				)
			}

			if expectedChecksum == "" {
				expectedChecksum = loaded.checksum
			}
			if loaded.checksum != expectedChecksum {
				t.Fatalf(
					"readMigration() checksum = %q, want %q",
					loaded.checksum,
					expectedChecksum,
				)
			}
		})
	}
}

func TestLoadMigrationsSortsByNumericVersion(t *testing.T) {
	files := fstest.MapFS{
		"000010_tenth.up.sql": {
			Data: []byte("SELECT 10;"),
		},
		"000002_second.up.sql": {
			Data: []byte("SELECT 2;"),
		},
		"000001_first.up.sql": {
			Data: []byte("SELECT 1;"),
		},
	}

	loaded, err := loadMigrations(files)
	if err != nil {
		t.Fatalf("loadMigrations() error = %v, want nil", err)
	}

	expectedVersions := []int64{1, 2, 10}
	if len(loaded) != len(expectedVersions) {
		t.Fatalf(
			"migration count = %d, want %d",
			len(loaded),
			len(expectedVersions),
		)
	}

	for index, expectedVersion := range expectedVersions {
		if loaded[index].version != expectedVersion {
			t.Fatalf(
				"migration[%d].version = %d, want %d",
				index,
				loaded[index].version,
				expectedVersion,
			)
		}
	}
}

func TestLoadMigrationsRejectsDuplicateVersion(t *testing.T) {
	files := fstest.MapFS{
		"000001_first.up.sql": {
			Data: []byte("SELECT 1;"),
		},
		"000001_duplicate.up.sql": {
			Data: []byte("SELECT 2;"),
		},
	}

	_, err := loadMigrations(files)
	if err == nil {
		t.Fatal("loadMigrations() error = nil, want duplicate-version error")
	}
}

func TestLoadMigrationsRejectsInvalidFilename(t *testing.T) {
	files := fstest.MapFS{
		"invalid.up.sql": {
			Data: []byte("SELECT 1;"),
		},
	}

	_, err := loadMigrations(files)
	if err == nil {
		t.Fatal("loadMigrations() error = nil, want invalid-filename error")
	}
}
