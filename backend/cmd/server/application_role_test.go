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

func TestValidateApplicationRoleFeaturesRejectsDisabledDedicatedWorker(t *testing.T) {
	testCases := []struct {
		name                   string
		role                   config.ApplicationRole
		embeddingWorkerEnabled bool
		answerEnabled          bool
		answerJobsEnabled      bool
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
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateApplicationRoleFeatures(
				testCase.role,
				testCase.embeddingWorkerEnabled,
				testCase.answerEnabled,
				testCase.answerJobsEnabled,
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
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateApplicationRoleFeatures(
				testCase.role,
				testCase.embeddingWorkerEnabled,
				testCase.answerEnabled,
				testCase.answerJobsEnabled,
			)
			if err != nil {
				t.Fatalf("validateApplicationRoleFeatures() error = %v, want nil", err)
			}
		})
	}
}
