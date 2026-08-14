package answer

import (
	"errors"
	"testing"
)

func TestResolveResponseLanguage(t *testing.T) {
	tests := []struct {
		name      string
		requested ResponseLanguage
		query     string
		wanted    ResponseLanguage
		wantedErr error
	}{
		{name: "omitted detects Chinese", query: "磁浮列车如何控制？", wanted: ResponseLanguageChinese},
		{name: "auto detects English", requested: ResponseLanguageAuto, query: "How does control work?", wanted: ResponseLanguageEnglish},
		{name: "Chinese technical terms remain Chinese", requested: ResponseLanguageAuto, query: "LSTM与MGD如何配合检测异常？", wanted: ResponseLanguageChinese},
		{name: "explicit English overrides Chinese query", requested: ResponseLanguageEnglish, query: "请解释控制方法", wanted: ResponseLanguageEnglish},
		{name: "normalized explicit Chinese", requested: ResponseLanguage(" ZH "), query: "English question", wanted: ResponseLanguageChinese},
		{name: "unknown text falls back Chinese", requested: ResponseLanguageAuto, query: "12345?!", wanted: ResponseLanguageChinese},
		{name: "unsupported language", requested: ResponseLanguage("ja"), query: "question", wantedErr: ErrInvalidResponseLanguage},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := resolveResponseLanguage(test.requested, test.query)
			if !errors.Is(err, test.wantedErr) {
				t.Fatalf("resolveResponseLanguage() error = %v, want %v", err, test.wantedErr)
			}
			if got != test.wanted {
				t.Fatalf("resolveResponseLanguage() = %q, want %q", got, test.wanted)
			}
		})
	}
}
