package agent

import (
	"encoding/json"
	"testing"
)

func TestModelDecisionRequiresExactlyOneBranchAndValidUsage(t *testing.T) {
	validUsage := TokenUsage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}
	if !(ModelDecision{
		ToolCall: &ModelToolCall{Name: ToolSearchDocuments, Arguments: json.RawMessage(`{"query":"x"}`)},
		Usage:    validUsage,
	}).IsValid() {
		t.Fatal("valid Tool decision must be accepted")
	}
	if !(ModelDecision{
		Final: &ModelFinalAnswer{Text: "answer", EvidenceRefs: []string{"step-1:item-1"}},
		Usage: validUsage,
	}).IsValid() {
		t.Fatal("valid Final decision must be accepted")
	}
	for name, decision := range map[string]ModelDecision{
		"no branch":     {Usage: validUsage},
		"both branches": {ToolCall: &ModelToolCall{Name: "x", Arguments: json.RawMessage(`{}`)}, Final: &ModelFinalAnswer{Text: "x"}, Usage: validUsage},
		"invalid usage": {Final: &ModelFinalAnswer{Text: "x"}, Usage: TokenUsage{PromptTokens: 2, CompletionTokens: 2, TotalTokens: 5}},
		"empty tool":    {ToolCall: &ModelToolCall{Name: "", Arguments: json.RawMessage(`{}`)}, Usage: validUsage},
		"empty final":   {Final: &ModelFinalAnswer{Text: "  "}, Usage: validUsage},
	} {
		t.Run(name, func(t *testing.T) {
			if decision.IsValid() {
				t.Fatalf("invalid decision accepted: %+v", decision)
			}
		})
	}
}

func TestAgentContextValidationExcludesUnknownRolesAndEmptyContent(t *testing.T) {
	valid := AgentContext{
		SystemInstruction: "bounded instruction",
		CurrentQuestion:   "question",
		History: []ModelMessage{
			{Role: "user", Content: "previous question"},
			{Role: "assistant", Content: "previous answer"},
		},
	}
	if !valid.IsValid() {
		t.Fatal("valid bounded context must be accepted")
	}
	invalidRole := valid
	invalidRole.History = []ModelMessage{{Role: "system", Content: "hidden"}}
	if invalidRole.IsValid() {
		t.Fatal("system history role must be rejected")
	}
	tooMany := valid
	tooMany.History = make([]ModelMessage, maxContextHistoryMessages+1)
	for index := range tooMany.History {
		tooMany.History[index] = ModelMessage{Role: "user", Content: "bounded"}
	}
	if tooMany.IsValid() {
		t.Fatal("oversized history message count must be rejected")
	}
}
