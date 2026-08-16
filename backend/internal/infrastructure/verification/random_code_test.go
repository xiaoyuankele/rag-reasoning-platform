package verification

import (
	"errors"
	"regexp"
	"testing"
)

func TestRandomCodeGeneratorGenerate(t *testing.T) {
	generator := NewRandomCodeGenerator()
	codePattern := regexp.MustCompile(`^[0-9]{6}$`)

	for index := 0; index < 100; index++ {
		code, err := generator.Generate()
		if err != nil {
			t.Fatalf("Generate() error = %v, want nil", err)
		}
		if !codePattern.MatchString(code) {
			t.Fatalf("Generate() code = %q, want exactly six digits", code)
		}
	}
}

func TestRandomCodeGeneratorPreservesRandomSourceError(t *testing.T) {
	randomErr := errors.New("random source unavailable")
	generator := &RandomCodeGenerator{
		reader: errorReader{err: randomErr},
	}

	_, err := generator.Generate()
	if !errors.Is(err, randomErr) {
		t.Fatalf("Generate() error = %v, want wrapped random source error", err)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read(_ []byte) (int, error) {
	return 0, r.err
}
