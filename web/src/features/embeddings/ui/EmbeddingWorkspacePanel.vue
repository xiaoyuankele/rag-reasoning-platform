<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import {
  canCancelEmbeddingJob,
  embeddingJobStatusLabels,
  type EmbeddingJob,
} from '../../../entities/embedding-job/model/embedding-job'
import type { DocumentStatus, ResearchDocument } from '../../../entities/document/model/document'
import { maximumBatchSize, useEmbeddingWorkspace } from '../model/use-embedding-workspace'
import type { EmbeddingDocumentPageLoader } from '../model/use-embedding-workspace'

const props = defineProps<{
  loadDocumentPage: EmbeddingDocumentPageLoader
}>()

const searchQuery = ref('')
const statusFilter = ref<DocumentStatus | 'all'>('all')
const isConfirmingSubmission = ref(false)

const {
  actionsByDocumentId,
  activeJobCount,
  cancel,
  clearSelection,
  documents,
  feedbackByDocumentId,
  initialize,
  isSubmitting,
  jobsByDocumentId,
  load,
  queueSelected,
  requestId,
  selectDocuments,
  selectedCount,
  selectedDocumentIds,
  state,
  toggleDocument,
  workspaceMessage,
} = useEmbeddingWorkspace({ loadDocumentPage: props.loadDocumentPage })

const documentStatusLabels: Record<DocumentStatus, string> = {
  uploaded: '等待解析',
  processing: '解析中',
  ready: '已解析',
  failed: '解析失败',
}

const filteredDocuments = computed(() => {
  const normalizedQuery = searchQuery.value.trim().toLocaleLowerCase()
  return documents.value.filter((document) => {
    if (statusFilter.value !== 'all' && document.status !== statusFilter.value) return false
    if (!normalizedQuery) return true
    return `${document.title ?? ''} ${document.originalName} ${document.id}`
      .toLocaleLowerCase()
      .includes(normalizedQuery)
  })
})

const selectableFilteredDocuments = computed(() =>
  filteredDocuments.value.filter(canSelectForQueue),
)
const allFilteredSelected = computed(
  () =>
    selectableFilteredDocuments.value.length > 0 &&
    selectableFilteredDocuments.value.every((document) =>
      selectedDocumentIds.value.has(document.id),
    ),
)

function displayTitle(document: ResearchDocument): string {
  return document.title?.trim() || document.originalName
}

function canSelectForQueue(document: ResearchDocument): boolean {
  const job = jobsByDocumentId.value.get(document.id)
  return !job || job.status === 'failed' || job.status === 'canceled'
}

function selectionHint(document: ResearchDocument): string {
  const job = jobsByDocumentId.value.get(document.id)
  if (!job) return '当前会话尚未跟踪向量任务'
  if (job.status === 'succeeded') return '已在当前会话完成向量化'
  if (job.status === 'failed' || job.status === 'canceled') return '可以重新申请向量化'
  return '已有活动任务，正在自动跟踪'
}

function toggleFilteredSelection(): void {
  if (allFilteredSelected.value) {
    for (const document of selectableFilteredDocuments.value) toggleDocument(document.id, false)
    return
  }
  selectDocuments(selectableFilteredDocuments.value.map((document) => document.id))
}

function requestQueueSubmission(): void {
  if (selectedCount.value > 0 && !isSubmitting.value) isConfirmingSubmission.value = true
}

function confirmQueueSubmission(): void {
  isConfirmingSubmission.value = false
  if (selectedCount.value === 0) return
  void queueSelected()
}

function formatDate(value: Date): string {
  return new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(value)
}

function jobDetail(job: EmbeddingJob): string {
  const details = [`${job.modelName} · ${job.dimensions} 维`, `尝试 ${job.attemptCount} 次`]
  if (job.totalTokens !== null) details.push(`${job.totalTokens} Tokens`)
  return details.join(' · ')
}

onMounted(() => void initialize())
</script>

<template>
  <section class="embedding-workspace" aria-labelledby="embedding-workspace-title">
    <div class="explanation-card">
      <div>
        <p class="section-kicker">Vector preparation</p>
        <h2 id="embedding-workspace-title">选择需要进入语义检索的文档</h2>
        <p>
          向量化会把解析后的文本块转换成可比较的数值表示。任务由后端异步执行，关闭本页不会中断任务。
        </p>
      </div>
      <dl>
        <div>
          <dt>单次上限</dt>
          <dd>{{ maximumBatchSize }} 份</dd>
        </div>
        <div>
          <dt>活动任务</dt>
          <dd>{{ activeJobCount }}</dd>
        </div>
        <div>
          <dt>已选择</dt>
          <dd>{{ selectedCount }}</dd>
        </div>
      </dl>
    </div>

    <div
      v-if="workspaceMessage"
      class="message-card"
      :class="`message-card--${workspaceMessage.kind}`"
      :role="workspaceMessage.kind === 'error' ? 'alert' : 'status'"
    >
      <span>{{ workspaceMessage.message }}</span>
      <small v-if="workspaceMessage.requestId">请求编号：{{ workspaceMessage.requestId }}</small>
    </div>

    <div class="workspace-toolbar">
      <label class="search-field">
        <span>筛选文档</span>
        <input v-model="searchQuery" type="search" placeholder="输入标题、文件名或文档 ID" />
      </label>
      <label class="status-field">
        <span>解析状态</span>
        <select v-model="statusFilter">
          <option value="all">全部状态</option>
          <option value="ready">已解析</option>
          <option value="processing">解析中</option>
          <option value="uploaded">等待解析</option>
          <option value="failed">解析失败</option>
        </select>
      </label>
      <div class="toolbar-actions">
        <button
          class="secondary-button"
          type="button"
          :disabled="selectableFilteredDocuments.length === 0"
          @click="toggleFilteredSelection"
        >
          {{ allFilteredSelected ? '取消当前筛选' : '选择当前筛选' }}
        </button>
        <button v-if="selectedCount > 0" class="text-button" type="button" @click="clearSelection">
          清空选择
        </button>
        <button
          class="primary-button"
          type="button"
          :disabled="selectedCount === 0 || isSubmitting"
          @click="requestQueueSubmission"
        >
          {{ isSubmitting ? '正在提交…' : `开始向量化（${selectedCount}）` }}
        </button>
      </div>
    </div>

    <div
      v-if="isConfirmingSubmission"
      class="confirmation-card"
      role="alertdialog"
      aria-labelledby="embedding-confirmation-title"
      aria-describedby="embedding-confirmation-description"
    >
      <div>
        <strong id="embedding-confirmation-title">确认提交 {{ selectedCount }} 份文档？</strong>
        <p id="embedding-confirmation-description">
          向量任务可能调用远程模型并消耗额度。后端尚不能按文档返回历史成功任务；当前会话未跟踪的文档如果过去已经完成向量化，
          本次可能重新生成。
        </p>
      </div>
      <div class="confirmation-actions">
        <button class="secondary-button" type="button" @click="isConfirmingSubmission = false">
          返回检查
        </button>
        <button class="primary-button" type="button" @click="confirmQueueSubmission">
          确认并提交
        </button>
      </div>
    </div>

    <div v-if="state === 'loading' || state === 'idle'" class="state-card" role="status">
      <span class="loading-dot" aria-hidden="true" />
      <div>
        <strong>正在读取文档与任务</strong>
        <p>会恢复当前浏览器会话记录的最近任务。</p>
      </div>
    </div>

    <div v-else-if="state === 'error'" class="state-card state-card--error" role="alert">
      <div>
        <strong>向量化工作区加载失败</strong>
        <p>{{ workspaceMessage?.message }}</p>
        <small v-if="requestId">请求编号：{{ requestId }}</small>
      </div>
      <button class="secondary-button" type="button" @click="load">重新加载</button>
    </div>

    <div v-else-if="state === 'empty'" class="state-card state-card--quiet">
      <div>
        <strong>还没有可管理的文档</strong>
        <p>请先到文档库上传并解析资料。</p>
      </div>
      <RouterLink class="secondary-link" to="/documents">前往文档库</RouterLink>
    </div>

    <div v-else-if="filteredDocuments.length === 0" class="state-card state-card--quiet">
      <div>
        <strong>没有符合筛选条件的文档</strong>
        <p>可以修改关键词或解析状态筛选。</p>
      </div>
    </div>

    <ol v-else class="document-list">
      <li v-for="document in filteredDocuments" :key="document.id" class="document-row">
        <label
          class="document-selection"
          :class="{ 'document-selection--disabled': !canSelectForQueue(document) }"
        >
          <input
            type="checkbox"
            :checked="selectedDocumentIds.has(document.id)"
            :disabled="!canSelectForQueue(document)"
            :aria-label="`选择 ${displayTitle(document)}`"
            @change="toggleDocument(document.id, ($event.target as HTMLInputElement).checked)"
          />
          <span aria-hidden="true" />
        </label>

        <div class="document-information">
          <div class="document-heading">
            <div>
              <h3>{{ displayTitle(document) }}</h3>
              <p v-if="document.title">{{ document.originalName }}</p>
            </div>
            <span class="document-status" :class="`document-status--${document.status}`">
              {{ documentStatusLabels[document.status] }}
            </span>
          </div>
          <div class="document-meta">
            <span>#{{ document.id }}</span>
            <span>更新于 {{ formatDate(document.updatedAt) }}</span>
            <span>{{ selectionHint(document) }}</span>
          </div>

          <div v-if="jobsByDocumentId.get(document.id)" class="job-card">
            <div class="job-main">
              <span
                class="job-status"
                :class="`job-status--${jobsByDocumentId.get(document.id)!.status}`"
              >
                {{ embeddingJobStatusLabels[jobsByDocumentId.get(document.id)!.status] }}
              </span>
              <div>
                <strong>任务 #{{ jobsByDocumentId.get(document.id)!.id }}</strong>
                <p>{{ jobDetail(jobsByDocumentId.get(document.id)!) }}</p>
              </div>
            </div>
            <button
              v-if="canCancelEmbeddingJob(jobsByDocumentId.get(document.id)!.status)"
              class="text-button text-button--danger"
              type="button"
              :disabled="actionsByDocumentId.has(document.id)"
              @click="cancel(document.id)"
            >
              {{ actionsByDocumentId.get(document.id) === 'cancelling' ? '取消中…' : '取消任务' }}
            </button>
          </div>

          <div
            v-if="feedbackByDocumentId.get(document.id)"
            class="row-feedback"
            :class="`row-feedback--${feedbackByDocumentId.get(document.id)!.kind}`"
            :role="feedbackByDocumentId.get(document.id)!.kind === 'error' ? 'alert' : 'status'"
          >
            <span>{{ feedbackByDocumentId.get(document.id)!.message }}</span>
            <small v-if="feedbackByDocumentId.get(document.id)!.requestId">
              请求编号：{{ feedbackByDocumentId.get(document.id)!.requestId }}
            </small>
          </div>
        </div>
      </li>
    </ol>

    <p class="session-note">
      页面只能恢复本浏览器会话已经记录的任务。后端提供任务列表接口前，“未跟踪”不等于“从未向量化”。
    </p>
  </section>
</template>

<style scoped>
.embedding-workspace {
  display: grid;
  gap: 18px;
}

.explanation-card {
  display: grid;
  grid-template-columns: 1fr;
  gap: 32px;
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface-subtle);
}

.section-kicker {
  margin-bottom: 8px;
  color: var(--color-accent);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.explanation-card h2 {
  margin-bottom: 9px;
  font-size: 20px;
  letter-spacing: -0.025em;
}

.explanation-card p:last-child,
.state-card p,
.job-card p {
  margin-bottom: 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.65;
}

.explanation-card dl {
  display: grid;
  width: 100%;
  min-width: 0;
  grid-template-columns: repeat(3, 1fr);
  align-self: center;
  margin: 0;
}

.explanation-card dl div {
  padding: 0 18px;
  border-left: 1px solid var(--color-border);
}

.explanation-card dt {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.explanation-card dd {
  margin: 6px 0 0;
  font-size: 18px;
  font-weight: 650;
}

.message-card,
.row-feedback {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 8px 18px;
  padding: 11px 14px;
  border-radius: 10px;
  font-size: 12px;
}

.confirmation-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 24px;
  padding: 18px;
  border: 1px solid #dccb9a;
  border-radius: 12px;
  background: #fff9e8;
}

.confirmation-card strong {
  font-size: 14px;
}

.confirmation-card p {
  max-width: 740px;
  margin: 6px 0 0;
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.6;
}

.confirmation-actions {
  display: flex;
  flex: 0 0 auto;
  gap: 10px;
}

.message-card--success,
.row-feedback--success {
  background: var(--color-accent-soft);
  color: var(--color-accent);
}

.message-card--error,
.row-feedback--error {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.workspace-toolbar {
  display: grid;
  grid-template-columns: minmax(220px, 1fr) minmax(130px, 160px);
  align-items: end;
  gap: 12px;
  padding: 16px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

.search-field,
.status-field {
  display: grid;
  gap: 6px;
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 650;
}

.search-field input,
.status-field select {
  min-height: 40px;
  padding: 0 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  outline: none;
}

.search-field input:focus,
.status-field select:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

.toolbar-actions {
  display: flex;
  grid-column: 1 / -1;
  align-items: center;
  justify-content: flex-start;
  gap: 10px;
}

.primary-button,
.secondary-button,
.secondary-link {
  display: inline-flex;
  min-height: 40px;
  align-items: center;
  justify-content: center;
  padding: 0 14px;
  border-radius: 9px;
  cursor: pointer;
  font-size: 13px;
  font-weight: 650;
  text-decoration: none;
}

.primary-button {
  border: 1px solid var(--color-accent);
  background: var(--color-accent);
  color: #fff;
}

.secondary-button,
.secondary-link {
  border: 1px solid var(--color-border-strong);
  background: var(--color-surface);
  color: var(--color-text-strong);
}

.primary-button:disabled,
.secondary-button:disabled,
.text-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.text-button {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-size: 12px;
  font-weight: 650;
}

.text-button--danger {
  color: var(--color-danger);
}

.state-card {
  display: flex;
  min-height: 112px;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  padding: 22px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

.state-card--quiet {
  border-style: dashed;
  background: var(--color-surface-subtle);
}

.state-card--error {
  border-color: #ead0cc;
  background: var(--color-danger-soft);
}

.loading-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  margin-right: 12px;
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 5px var(--color-accent-soft);
}

.state-card:has(.loading-dot) {
  justify-content: flex-start;
}

.document-list {
  display: grid;
  gap: 8px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.document-row {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr);
  gap: 16px;
  padding: 18px;
  border: 1px solid var(--color-border);
  border-radius: 13px;
  background: var(--color-surface);
}

.document-selection {
  padding-top: 3px;
  cursor: pointer;
}

.document-selection input {
  width: 17px;
  height: 17px;
  margin: 0;
  accent-color: var(--color-accent);
}

.document-selection--disabled {
  cursor: not-allowed;
  opacity: 0.45;
}

.document-information {
  min-width: 0;
}

.document-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 18px;
}

.document-heading h3 {
  overflow: hidden;
  margin-bottom: 4px;
  font-size: 14px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.document-heading p {
  overflow: hidden;
  margin-bottom: 0;
  color: var(--color-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.document-status,
.job-status {
  display: inline-flex;
  width: fit-content;
  padding: 5px 9px;
  border-radius: 999px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 650;
  white-space: nowrap;
}

.document-status--ready,
.job-status--succeeded {
  background: var(--color-accent-soft);
  color: var(--color-accent);
}

.document-status--failed,
.job-status--failed {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.job-status--processing,
.job-status--queued,
.job-status--waiting_document {
  background: #eef1f7;
  color: #4c5e7a;
}

.document-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
  margin-top: 10px;
  color: var(--color-text-subtle);
  font-size: 11px;
}

.job-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  margin-top: 14px;
  padding: 12px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-surface-subtle);
}

.job-main {
  display: flex;
  align-items: center;
  gap: 12px;
}

.job-main strong {
  font-size: 12px;
}

.job-main p {
  margin-top: 2px;
  font-size: 11px;
}

.row-feedback {
  margin-top: 10px;
}

.session-note {
  margin: 2px 0 0;
  color: var(--color-text-subtle);
  font-size: 11px;
  line-height: 1.6;
}

@media (max-width: 700px) {
  .explanation-card,
  .workspace-toolbar {
    grid-template-columns: 1fr;
  }

  .explanation-card dl {
    width: 100%;
    min-width: 0;
  }

  .workspace-toolbar .toolbar-actions {
    grid-column: auto;
    flex-wrap: wrap;
  }

  .document-heading,
  .job-card,
  .confirmation-card {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
