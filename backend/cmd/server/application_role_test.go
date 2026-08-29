package main

import (
	"testing"

	"rag-reasoning-platform/backend/internal/config"
)

func TestValidateImplementedApplicationRoleAllowsAll(t *testing.T) {
	if err := validateImplementedApplicationRole(config.ApplicationRoleAll); err != nil {
		t.Fatalf("validateImplementedApplicationRole(all) error = %v, want nil", err)
	}
}

func TestValidateImplementedApplicationRoleRejectsReservedSplitRoles(t *testing.T) {
	roles := []config.ApplicationRole{
		config.ApplicationRoleAPI,
		config.ApplicationRoleDocumentWorker,
		config.ApplicationRoleEmbeddingWorker,
		config.ApplicationRoleAnswerWorker,
	}

	for _, role := range roles {
		t.Run(string(role), func(t *testing.T) {
			if err := validateImplementedApplicationRole(role); err == nil {
				t.Fatalf(
					"validateImplementedApplicationRole(%q) error = nil, want fail-fast",
					role,
				)
			}
		})
	}
}
