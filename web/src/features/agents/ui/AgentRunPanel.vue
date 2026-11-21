<script setup lang="ts">
import { computed } from 'vue'
import {
  agentRunStatusLabels,
  agentStepStatusLabels,
  agentToolLabels,
  canCancelAgentRun,
  type AgentRun,
  type AgentStep,
} from '../../../entities/agent/model/agent-run'
import type { AgentWorkspaceState } from '../model/use-agent-run-workspace'

const props = defineProps<{
  run: AgentRun
  state: AgentWorkspaceState
  errorMessage?: string
  requestId?: string
  readOnly?: boolean
  manualRestoreRequired?: boolean
}>()

const emit = defineEmits<{
  cancel: []
  restore: []
}>()

const titleId = computed(() => `agent-run-${props.run.id}-title`)
const showCancel = computed(() => canCancelAgentRun(props.run))

function stepLabel(step: AgentStep): string {
  if (step.kind === 'final') return '生成最终回答'
  return step.toolName ? agentToolLabels[step.toolName] : '受控工具'
}

function durationLabel(step: AgentStep): string {
  if (step.startedAt === null) return '尚未开始'
  const end = step.completedAt?.getTime() ?? Date.now()
  const milliseconds = Math.max(0, end - step.startedAt.getTime())
  if (milliseconds < 1_000) return `${milliseconds} ms`
  return `${(milliseconds / 1_000).toFixed(milliseconds < 10_000 ? 1 : 0)} s`
}

function sourceTitle(title: string, documentId: number): string {
  return title.trim() || `文档 #${documentId}`
}

function sourcePageLabel(pageStart: number | null, pageEnd: number | null): string {
  if (pageStart === null || pageEnd === null) return '页码未知'
  return pageStart === pageEnd ? `第 ${pageStart} 页` : `第 ${pageStart}–${pageEnd} 页`
}
</script>

<template>
  <section class="agent-run-panel" :aria-labelledby="titleId">
    <header class="agent-run-header">
      <div>
        <span class="agent-kicker">Agent Run #{{ run.id }}</span>
        <h3 :id="titleId">{{ agentRunStatusLabels[run.status] }}</h3>
      </div>
      <span class="agent-progress">{{ run.stepCount }} / {{ run.maxSteps }} 步</span>
    </header>

    <dl class="agent-budget" aria-label="Agent Token 用量与预算">
      <div>
        <dt>Prompt</dt>
        <dd>{{ run.tokenUsage.promptTokens }}</dd>
      </div>
      <div>
        <dt>Completion</dt>
        <dd>{{ run.tokenUsage.completionTokens }}</dd>
      </div>
      <div>
        <dt>Total / Budget</dt>
        <dd>{{ run.tokenUsage.totalTokens }} / {{ run.maxTotalTokens }}</dd>
      </div>
    </dl>

    <ol v-if="run.steps.length > 0" class="agent-timeline" aria-label="Agent 执行时间线">
      <li v-for="step in run.steps" :key="step.id" class="agent-step">
        <span class="agent-step-number">{{ step.sequenceNo }}</span>
        <div>
          <strong>{{ stepLabel(step) }}</strong>
          <span>
            {{ agentStepStatusLabels[step.status] }} · {{ durationLabel(step) }} ·
            {{ step.tokenUsage.totalTokens }} Tokens
          </span>
          <p v-if="step.error" class="agent-step-error">{{ step.error.message }}</p>
        </div>
      </li>
    </ol>
    <p v-else class="agent-empty-steps">
      {{ run.status === 'queued' ? '等待 Worker 领取任务。' : '尚无可展示的执行步骤。' }}
    </p>

    <section v-if="run.status === 'succeeded' && run.result" class="agent-result">
      <h4>最终回答</h4>
      <p class="agent-answer">{{ run.result.answer }}</p>
      <ol class="agent-sources" aria-label="Agent 回答来源">
        <li v-for="source in run.result.sources" :key="source.evidenceRef">
          <strong
            >[{{ source.citation }}] {{ sourceTitle(source.title, source.documentId) }}</strong
          >
          <span>
            {{ sourcePageLabel(source.pageStart, source.pageEnd) }} · 文档 #{{
              source.documentId
            }}
            · 文本块 #{{ source.chunkId }}
          </span>
        </li>
      </ol>
    </section>

    <p v-if="run.status === 'failed' && run.error" class="agent-run-error" role="alert">
      {{ run.error.message }}
    </p>
    <p v-else-if="run.status === 'canceled'" class="agent-run-note">Agent Run 已取消。</p>

    <div class="agent-actions">
      <button
        v-if="showCancel"
        type="button"
        class="agent-cancel-button"
        :disabled="readOnly || state === 'canceling'"
        @click="emit('cancel')"
      >
        {{ state === 'canceling' ? '取消中…' : '取消 Agent Run' }}
      </button>
      <span v-else-if="run.status === 'processing'" class="agent-run-note"
        >执行中，当前不可取消。</span
      >
      <button
        v-if="manualRestoreRequired"
        type="button"
        class="agent-restore-button"
        :disabled="state === 'restoring'"
        @click="emit('restore')"
      >
        {{ state === 'restoring' ? '恢复中…' : '重新读取状态' }}
      </button>
    </div>

    <p v-if="errorMessage" class="agent-request-error" role="alert">
      {{ errorMessage }}
      <span v-if="requestId">请求编号：{{ requestId }}</span>
    </p>
  </section>
</template>

<style scoped>
.agent-run-panel {
  display: grid;
  gap: 16px;
  padding: 18px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface-muted);
}

.agent-run-header,
.agent-actions {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}

.agent-run-header h3,
.agent-result h4,
.agent-answer,
.agent-step-error,
.agent-run-error,
.agent-run-note,
.agent-request-error,
.agent-empty-steps {
  margin: 0;
}

.agent-kicker,
.agent-progress,
.agent-step span,
.agent-sources span,
.agent-run-note,
.agent-empty-steps {
  color: var(--color-text-muted);
  font-size: 0.84rem;
}

.agent-budget {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 10px;
  margin: 0;
}

.agent-budget div {
  padding: 10px;
  border-radius: 10px;
  background: var(--color-surface);
}

.agent-budget dt {
  color: var(--color-text-muted);
  font-size: 0.75rem;
}

.agent-budget dd {
  margin: 4px 0 0;
  font-weight: 700;
}

.agent-timeline,
.agent-sources {
  display: grid;
  gap: 10px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.agent-step {
  display: grid;
  grid-template-columns: 30px minmax(0, 1fr);
  gap: 10px;
}

.agent-step > div,
.agent-sources li {
  display: grid;
  gap: 4px;
}

.agent-step-number {
  display: grid;
  width: 28px;
  height: 28px;
  place-items: center;
  border-radius: 50%;
  background: var(--color-surface-active);
  font-weight: 700;
}

.agent-result {
  display: grid;
  gap: 10px;
  padding-top: 14px;
  border-top: 1px solid var(--color-border);
}

.agent-answer {
  white-space: pre-wrap;
}

.agent-step-error,
.agent-run-error,
.agent-request-error {
  color: var(--color-danger);
}

.agent-request-error {
  display: grid;
  gap: 4px;
}

@media (max-width: 720px) {
  .agent-budget {
    grid-template-columns: 1fr;
  }

  .agent-run-header,
  .agent-actions {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
