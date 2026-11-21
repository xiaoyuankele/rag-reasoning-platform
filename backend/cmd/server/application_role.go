package main

import (
	"fmt"

	"rag-reasoning-platform/backend/internal/config"
)

// applicationRolePlan 是组合根使用的组件启动清单。
//
// 它不表示业务状态，也不会传入 Application 或 Domain。main 只用它决定
// 本次进程需要加载哪些配置、创建哪些基础设施以及启动哪些生命周期组件。
type applicationRolePlan struct {
	serveHTTP          bool
	runDocumentWorker  bool
	runEmbeddingWorker bool
	runAnswerWorker    bool
	runAgentWorker     bool
}

// newApplicationRolePlan 把稳定的 APP_ROLE 转换成组件能力矩阵。
func newApplicationRolePlan(
	role config.ApplicationRole,
) (applicationRolePlan, error) {
	switch role {
	case config.ApplicationRoleAll:
		return applicationRolePlan{
			serveHTTP:          true,
			runDocumentWorker:  true,
			runEmbeddingWorker: true,
			runAnswerWorker:    true,
			runAgentWorker:     true,
		}, nil

	case config.ApplicationRoleAPI:
		return applicationRolePlan{serveHTTP: true}, nil

	case config.ApplicationRoleDocumentWorker:
		return applicationRolePlan{runDocumentWorker: true}, nil

	case config.ApplicationRoleEmbeddingWorker:
		return applicationRolePlan{runEmbeddingWorker: true}, nil

	case config.ApplicationRoleAnswerWorker:
		return applicationRolePlan{runAnswerWorker: true}, nil

	case config.ApplicationRoleAgentWorker:
		return applicationRolePlan{runAgentWorker: true}, nil

	default:
		return applicationRolePlan{}, fmt.Errorf(
			"build component plan for unsupported application role %q",
			role,
		)
	}
}

func (p applicationRolePlan) needsStorage() bool {
	return p.serveHTTP || p.runDocumentWorker
}

func (p applicationRolePlan) needsDocumentConfig() bool {
	return p.serveHTTP || p.runDocumentWorker
}

func (p applicationRolePlan) needsEmbeddingConfig() bool {
	return p.serveHTTP || p.runEmbeddingWorker || p.runAnswerWorker || p.runAgentWorker
}

func (p applicationRolePlan) needsGenerationConfig() bool {
	// Agent Worker 不构造 Answer，但必须读取同一 provider/model 的 Answer
	// capacity 配置，以便跨独立进程拒绝共享 key 下的不一致限制。
	return p.serveHTTP || p.runAnswerWorker || p.runAgentWorker
}

func (p applicationRolePlan) needsAnswerJobsConfig() bool {
	return p.serveHTTP || p.runAnswerWorker
}

func (p applicationRolePlan) needsCacheConfig() bool {
	return p.serveHTTP || p.runAnswerWorker
}

func (p applicationRolePlan) needsCapacityCoordinationConfig() bool {
	return p.serveHTTP || p.runEmbeddingWorker || p.runAnswerWorker || p.runAgentWorker
}

func (p applicationRolePlan) needsAgentConfig() bool {
	return p.serveHTTP || p.runAgentWorker
}

// validateApplicationRoleFeatures 拒绝“角色已启动但核心循环被功能开关关闭”的
// 空壳进程。all/api 仍允许按原有功能开关选择能力；专用 Worker 角色必须明确
// 打开对应功能。
func validateApplicationRoleFeatures(
	role config.ApplicationRole,
	embeddingWorkerEnabled bool,
	answerEnabled bool,
	answerJobsEnabled bool,
	agentWorkerEnabled bool,
) error {
	switch role {
	case config.ApplicationRoleEmbeddingWorker:
		if !embeddingWorkerEnabled {
			return fmt.Errorf(
				"APP_ROLE=embedding-worker requires EMBEDDING_WORKER_ENABLED=true",
			)
		}

	case config.ApplicationRoleAnswerWorker:
		if !answerEnabled || !answerJobsEnabled {
			return fmt.Errorf(
				"APP_ROLE=answer-worker requires ANSWER_JOBS_ENABLED=true and ANSWER_ENABLED=true",
			)
		}

	case config.ApplicationRoleAgentWorker:
		if !agentWorkerEnabled {
			return fmt.Errorf(
				"APP_ROLE=agent-worker requires AGENT_WORKER_ENABLED=true",
			)
		}
	}

	return nil
}
