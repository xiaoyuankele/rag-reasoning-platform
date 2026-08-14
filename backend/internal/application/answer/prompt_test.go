package answer

import (
	"errors"
	"strings"
	"testing"

	documentdomain "rag-reasoning-platform/backend/internal/domain/document"
)

func TestBuildUserPromptPreservesOrderAndSourceMetadata(t *testing.T) {
	title := "Real-Time Malfunction Detection"
	pageOne := 1
	pageThree := 3
	pageFour := 4
	hits := []documentdomain.SemanticSearchHit{
		{
			ChunkID:      101,
			DocumentID:   208,
			Title:        &title,
			OriginalName: "mathematics-11-04045-v2.pdf",
			Content:      "first evidence content",
			PageStart:    &pageOne,
			PageEnd:      &pageOne,
		},
		{
			ChunkID:      102,
			DocumentID:   208,
			OriginalName: "fallback-title.pdf",
			Content:      "second evidence content",
			PageStart:    &pageThree,
			PageEnd:      &pageFour,
		},
	}

	prompt, err := buildUserPrompt(
		"  怎样检测故障？  ",
		hits,
		ResponseLanguageChinese,
	)
	if err != nil {
		t.Fatalf("buildUserPrompt() error = %v, want nil", err)
	}

	if strings.Count(prompt, "怎样检测故障？") != 1 {
		t.Fatalf("query must appear exactly once:\n%s", prompt)
	}
	firstPosition := strings.Index(prompt, "=== 证据 [1] 开始 ===")
	secondPosition := strings.Index(prompt, "=== 证据 [2] 开始 ===")
	if firstPosition < 0 || secondPosition <= firstPosition {
		t.Fatalf("evidence order is not stable:\n%s", prompt)
	}

	for _, wanted := range []string{
		"文献标题：Real-Time Malfunction Detection",
		"原始文件名：mathematics-11-04045-v2.pdf",
		"页码：第 1 页",
		"first evidence content",
		"文献标题：fallback-title.pdf",
		"原始文件名：fallback-title.pdf",
		"页码：第 3-4 页",
		"second evidence content",
	} {
		if !strings.Contains(prompt, wanted) {
			t.Fatalf("prompt does not contain %q:\n%s", wanted, prompt)
		}
	}
	if prompt != strings.TrimSpace(prompt) {
		t.Fatal("prompt contains leading or trailing whitespace")
	}
}

func TestBuildUserPromptRejectsMissingInput(t *testing.T) {
	validHits := []documentdomain.SemanticSearchHit{{Content: "evidence"}}

	if _, err := buildUserPrompt(
		" ",
		validHits,
		ResponseLanguageChinese,
	); !errors.Is(
		err,
		errAnswerPromptQueryRequired,
	) {
		t.Fatalf("blank query error = %v, want query required", err)
	}
	if _, err := buildUserPrompt(
		"question",
		nil,
		ResponseLanguageEnglish,
	); !errors.Is(
		err,
		errAnswerPromptEvidenceRequired,
	) {
		t.Fatalf("empty hits error = %v, want evidence required", err)
	}
}

func TestFormatPageRange(t *testing.T) {
	pageTwo := 2
	pageFour := 4

	tests := []struct {
		name      string
		pageStart *int
		pageEnd   *int
		wanted    string
	}{
		{name: "unknown", wanted: "未知"},
		{name: "start only", pageStart: &pageTwo, wanted: "第 2 页"},
		{name: "end only", pageEnd: &pageFour, wanted: "第 4 页"},
		{name: "same page", pageStart: &pageTwo, pageEnd: &pageTwo, wanted: "第 2 页"},
		{name: "page range", pageStart: &pageTwo, pageEnd: &pageFour, wanted: "第 2-4 页"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := formatPageRange(
				test.pageStart,
				test.pageEnd,
				ResponseLanguageChinese,
			); got != test.wanted {
				t.Fatalf("formatPageRange() = %q, want %q", got, test.wanted)
			}
		})
	}
}

func TestBuildUserPromptUsesEnglishLabels(t *testing.T) {
	page := 2
	hits := []documentdomain.SemanticSearchHit{
		{
			OriginalName: "paper.pdf",
			Content:      "English evidence.",
			PageStart:    &page,
			PageEnd:      &page,
		},
	}

	prompt, err := buildUserPrompt(
		"How does it work?",
		hits,
		ResponseLanguageEnglish,
	)
	if err != nil {
		t.Fatalf("buildUserPrompt() error = %v, want nil", err)
	}

	for _, wanted := range []string{
		"User question:",
		"Retrieved evidence:",
		"=== Evidence [1] begins ===",
		"Document title: paper.pdf",
		"Original filename: paper.pdf",
		"Page: page 2",
		"Content:\nEnglish evidence.",
		"=== Evidence [1] ends ===",
	} {
		if !strings.Contains(prompt, wanted) {
			t.Fatalf("English prompt does not contain %q:\n%s", wanted, prompt)
		}
	}
	if !strings.Contains(
		buildSystemInstruction(ResponseLanguageEnglish),
		"must answer in English",
	) {
		t.Fatal("English system instruction does not require English output")
	}
}
