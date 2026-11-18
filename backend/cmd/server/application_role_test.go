package main

import (
	"reflect"
	"testing"

	"rag-reasoning-platform/backend/internal/config"
)

func TestNewApplicationRolePlanReturnsExpectedComponents(t *testing.T) {
	testCases := []struct {
		name string
		role config.ApplicationRole
		want applicationRolePlan
	}{
		{
			name: "all",
			role: config.ApplicationRoleAll,
			want: applicationRolePlan{
				serveHTTP:          true,
				runDocumentWorker:  true,
				runEmbeddingWorker: true,
				runAnswerWorker:    true,
				runAgentWorker:     true,
			},
		},
		{name: "api", role: config.ApplicationRoleAPI, want: applicationRolePlan{serveHTTP: true}},
		{
			name: "document worker",
			role: config.ApplicationRoleDocumentWorker,
			want: applicationRolePlan{runDocumentWorker: true},
		},
		{
			name: "embedding worker",
			role: config.ApplicationRoleEmbeddingWorker,
			want: applicationRolePlan{runEmbeddingWorker: true},
		},
		{
			name: "answer worker",
			role: config.ApplicationRoleAnswerWorker,
			want: applicationRolePlan{runAnswerWorker: true},
		},
		{
			name: "Agent worker",
			role: config.ApplicationRoleAgentWorker,
			want: applicationRolePlan{runAgentWorker: true},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := newApplicationRolePlan(testCase.role)
			if err != nil {
				t.Fatalf("newApplicationRolePlan(%q) error = %v", testCase.role, err)
			}
			if !reflect.DeepEqual(got, testCase.want) {
				t.Fatalf("newApplicationRolePlan(%q) = %+v, want %+v", testCase.role, got, testCase.want)
			}
		})
	}
}

func TestNewApplicationRolePlanRejectsUnsupportedRole(t *testing.T) {
	if _, err := newApplicationRolePlan("unknown"); err == nil {
		t.Fatal("newApplicationRolePlan(unknown) error = nil, want error")
	}
}

func TestAgentWorkerRoleLoadsOnlyRuntimeAndCapacityDependencies(t *testing.T) {
	plan, err := newApplicationRolePlan(config.ApplicationRoleAgentWorker)
	if err != nil {
		t.Fatalf("newApplicationRolePlan() error = %v", err)
	}
	if plan.serveHTTP || plan.runDocumentWorker || plan.runEmbeddingWorker || plan.runAnswerWorker ||
		!plan.runAgentWorker || plan.needsStorage() || plan.needsDocumentConfig() ||
		plan.needsAnswerJobsConfig() || plan.needsCacheConfig() ||
		!plan.needsEmbeddingConfig() || !plan.needsCapacityCoordinationConfig() ||
		!plan.needsAgentConfig() || !plan.needsGenerationConfig() {
		t.Fatalf("Agent Worker dependency plan = %+v", plan)
	}
	// Generation config is loaded only to compare the shared provider/model
	// capacity limits. The role plan still never constructs Answer components.
}

func TestValidateApplicationRoleFeaturesRejectsDisabledDedicatedWorker(t *testing.T) {
	testCases := []struct {
		name                   string
		role                   config.ApplicationRole
		embeddingWorkerEnabled bool
		answerEnabled          bool
		answerJobsEnabled      bool
		agentWorkerEnabled     bool
	}{
		{
			name: "embedding worker disabled",
			role: config.ApplicationRoleEmbeddingWorker,
		},
		{
			name:              "answer generation disabled",
			role:              config.ApplicationRoleAnswerWorker,
			answerJobsEnabled: true,
		},
		{
			name:          "answer jobs disabled",
			role:          config.ApplicationRoleAnswerWorker,
			answerEnabled: true,
		},
		{
			name: "Agent worker disabled",
			role: config.ApplicationRoleAgentWorker,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateApplicationRoleFeatures(
				testCase.role,
				testCase.embeddingWorkerEnabled,
				testCase.answerEnabled,
				testCase.answerJobsEnabled,
				testCase.agentWorkerEnabled,
			)
			if err == nil {
				t.Fatal("validateApplicationRoleFeatures() error = nil, want error")
			}
		})
	}
}

func TestValidateApplicationRoleFeaturesAcceptsRunnableRoles(t *testing.T) {
	testCases := []struct {
		name                   string
		role                   config.ApplicationRole
		embeddingWorkerEnabled bool
		answerEnabled          bool
		answerJobsEnabled      bool
		agentWorkerEnabled     bool
	}{
		{name: "all keeps feature flags optional", role: config.ApplicationRoleAll},
		{name: "api keeps feature flags optional", role: config.ApplicationRoleAPI},
		{name: "document worker", role: config.ApplicationRoleDocumentWorker},
		{
			name:                   "embedding worker enabled",
			role:                   config.ApplicationRoleEmbeddingWorker,
			embeddingWorkerEnabled: true,
		},
		{
			name:              "answer worker enabled",
			role:              config.ApplicationRoleAnswerWorker,
			answerEnabled:     true,
			answerJobsEnabled: true,
		},
		{
			name:               "Agent worker enabled",
			role:               config.ApplicationRoleAgentWorker,
			agentWorkerEnabled: true,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateApplicationRoleFeatures(
				testCase.role,
				testCase.embeddingWorkerEnabled,
				testCase.answerEnabled,
				testCase.answerJobsEnabled,
				testCase.agentWorkerEnabled,
			)
			if err != nil {
				t.Fatalf("validateApplicationRoleFeatures() error = %v, want nil", err)
			}
		})
	}
}
