package config

import (
	"path/filepath"
	"testing"
)

func TestLoadPythonUsesDefaults(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("PYTHON_EXECUTABLE", "")
	t.Setenv("PYTHON_SOURCE_ROOT", "")
	t.Setenv("PYTHON_PDF_MAX_FILE_SIZE_BYTES", "")
	t.Setenv("PYTHON_PDF_MAX_PAGES", "")
	t.Setenv("PYTHON_PROCESS_MODE", "")
	t.Setenv("PYTHON_PROCESS_POOL_SIZE", "")
	t.Setenv("PYTHON_PROCESS_MAX_DOCUMENTS", "")

	pythonConfig, err := LoadPython(appRoot)
	if err != nil {
		t.Fatalf("LoadPython() error = %v, want nil", err)
	}

	if pythonConfig.Executable != defaultPythonExecutable {
		t.Fatalf(
			"Executable = %q, want %q",
			pythonConfig.Executable,
			defaultPythonExecutable,
		)
	}
	expectedSourceRoot := filepath.Join(appRoot, "ai", "src")
	if pythonConfig.SourceRoot != expectedSourceRoot {
		t.Fatalf(
			"SourceRoot = %q, want %q",
			pythonConfig.SourceRoot,
			expectedSourceRoot,
		)
	}
	if pythonConfig.PDFMaxFileSizeBytes != defaultPythonPDFMaxFileSizeBytes {
		t.Fatalf(
			"PDFMaxFileSizeBytes = %d, want %d",
			pythonConfig.PDFMaxFileSizeBytes,
			defaultPythonPDFMaxFileSizeBytes,
		)
	}
	if pythonConfig.PDFMaxPages != defaultPythonPDFMaxPages {
		t.Fatalf(
			"PDFMaxPages = %d, want %d",
			pythonConfig.PDFMaxPages,
			defaultPythonPDFMaxPages,
		)
	}
	if pythonConfig.ProcessMode != PythonProcessModeOneShot {
		t.Fatalf(
			"ProcessMode = %q, want %q",
			pythonConfig.ProcessMode,
			PythonProcessModeOneShot,
		)
	}
	if pythonConfig.ProcessPoolSize != defaultPythonProcessPoolSize {
		t.Fatalf(
			"ProcessPoolSize = %d, want %d",
			pythonConfig.ProcessPoolSize,
			defaultPythonProcessPoolSize,
		)
	}
	if pythonConfig.ProcessMaxDocuments != defaultPythonProcessMaxDocuments {
		t.Fatalf(
			"ProcessMaxDocuments = %d, want %d",
			pythonConfig.ProcessMaxDocuments,
			defaultPythonProcessMaxDocuments,
		)
	}
}

func TestLoadPythonUsesEnvironment(t *testing.T) {
	appRoot := t.TempDir()
	t.Setenv("PYTHON_EXECUTABLE", " E:/dev/python/python.exe ")
	t.Setenv("PYTHON_SOURCE_ROOT", " ../custom-ai/src ")
	t.Setenv("PYTHON_PDF_MAX_FILE_SIZE_BYTES", " 1048576 ")
	t.Setenv("PYTHON_PDF_MAX_PAGES", " 25 ")
	t.Setenv("PYTHON_PROCESS_MODE", " POOL ")
	t.Setenv("PYTHON_PROCESS_POOL_SIZE", " 2 ")
	t.Setenv("PYTHON_PROCESS_MAX_DOCUMENTS", " 20 ")

	pythonConfig, err := LoadPython(appRoot)
	if err != nil {
		t.Fatalf("LoadPython() error = %v, want nil", err)
	}

	if pythonConfig.Executable != "E:/dev/python/python.exe" {
		t.Fatalf(
			"Executable = %q, want trimmed environment value",
			pythonConfig.Executable,
		)
	}
	expectedSourceRoot := filepath.Clean(filepath.Join(appRoot, "../custom-ai/src"))
	if pythonConfig.SourceRoot != expectedSourceRoot {
		t.Fatalf(
			"SourceRoot = %q, want %q",
			pythonConfig.SourceRoot,
			expectedSourceRoot,
		)
	}
	if pythonConfig.PDFMaxFileSizeBytes != 1048576 {
		t.Fatalf(
			"PDFMaxFileSizeBytes = %d, want 1048576",
			pythonConfig.PDFMaxFileSizeBytes,
		)
	}
	if pythonConfig.PDFMaxPages != 25 {
		t.Fatalf("PDFMaxPages = %d, want 25", pythonConfig.PDFMaxPages)
	}
	if pythonConfig.ProcessMode != PythonProcessModePool {
		t.Fatalf("ProcessMode = %q, want pool", pythonConfig.ProcessMode)
	}
	if pythonConfig.ProcessPoolSize != 2 {
		t.Fatalf("ProcessPoolSize = %d, want 2", pythonConfig.ProcessPoolSize)
	}
	if pythonConfig.ProcessMaxDocuments != 20 {
		t.Fatalf(
			"ProcessMaxDocuments = %d, want 20",
			pythonConfig.ProcessMaxDocuments,
		)
	}
}

func TestLoadPythonRejectsInvalidPDFLimits(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "non numeric file size", env: "PYTHON_PDF_MAX_FILE_SIZE_BYTES", value: "large"},
		{name: "zero file size", env: "PYTHON_PDF_MAX_FILE_SIZE_BYTES", value: "0"},
		{name: "file size above maximum", env: "PYTHON_PDF_MAX_FILE_SIZE_BYTES", value: "1073741825"},
		{name: "non numeric pages", env: "PYTHON_PDF_MAX_PAGES", value: "many"},
		{name: "zero pages", env: "PYTHON_PDF_MAX_PAGES", value: "0"},
		{name: "pages above maximum", env: "PYTHON_PDF_MAX_PAGES", value: "10001"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PYTHON_PDF_MAX_FILE_SIZE_BYTES", "")
			t.Setenv("PYTHON_PDF_MAX_PAGES", "")
			t.Setenv("PYTHON_PROCESS_MODE", "")
			t.Setenv("PYTHON_PROCESS_POOL_SIZE", "")
			t.Setenv("PYTHON_PROCESS_MAX_DOCUMENTS", "")
			t.Setenv(test.env, test.value)

			pythonConfig, err := LoadPython(t.TempDir())
			if err == nil {
				t.Fatalf("LoadPython() error = nil for %s=%q", test.env, test.value)
			}
			if pythonConfig != (PythonConfig{}) {
				t.Fatalf("LoadPython() config = %+v, want zero value", pythonConfig)
			}
		})
	}
}

func TestLoadPythonRejectsInvalidProcessPoolConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		env   string
		value string
	}{
		{name: "unknown mode", env: "PYTHON_PROCESS_MODE", value: "daemon"},
		{name: "zero pool size", env: "PYTHON_PROCESS_POOL_SIZE", value: "0"},
		{name: "pool size above maximum", env: "PYTHON_PROCESS_POOL_SIZE", value: "5"},
		{name: "zero max documents", env: "PYTHON_PROCESS_MAX_DOCUMENTS", value: "0"},
		{name: "non numeric max documents", env: "PYTHON_PROCESS_MAX_DOCUMENTS", value: "many"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("PYTHON_PDF_MAX_FILE_SIZE_BYTES", "")
			t.Setenv("PYTHON_PDF_MAX_PAGES", "")
			t.Setenv("PYTHON_PROCESS_MODE", "")
			t.Setenv("PYTHON_PROCESS_POOL_SIZE", "")
			t.Setenv("PYTHON_PROCESS_MAX_DOCUMENTS", "")
			t.Setenv(test.env, test.value)

			pythonConfig, err := LoadPython(t.TempDir())
			if err == nil {
				t.Fatalf("LoadPython() error = nil for %s=%q", test.env, test.value)
			}
			if pythonConfig != (PythonConfig{}) {
				t.Fatalf("LoadPython() config = %+v, want zero value", pythonConfig)
			}
		})
	}
}

// TestLoadPythonPreservesAbsoluteSourceRoot 验证部署环境提供的绝对 Python
// 源码路径不会再次相对于 APP_ROOT 拼接。
func TestLoadPythonPreservesAbsoluteSourceRoot(t *testing.T) {
	appRoot := t.TempDir()
	absoluteSourceRoot := t.TempDir()
	t.Setenv("PYTHON_SOURCE_ROOT", absoluteSourceRoot)
	t.Setenv("PYTHON_PDF_MAX_FILE_SIZE_BYTES", "")
	t.Setenv("PYTHON_PDF_MAX_PAGES", "")
	t.Setenv("PYTHON_PROCESS_MODE", "")
	t.Setenv("PYTHON_PROCESS_POOL_SIZE", "")
	t.Setenv("PYTHON_PROCESS_MAX_DOCUMENTS", "")

	pythonConfig, err := LoadPython(appRoot)
	if err != nil {
		t.Fatalf("LoadPython() error = %v, want nil", err)
	}
	if pythonConfig.SourceRoot != filepath.Clean(absoluteSourceRoot) {
		t.Fatalf(
			"SourceRoot = %q, want %q",
			pythonConfig.SourceRoot,
			filepath.Clean(absoluteSourceRoot),
		)
	}
}
