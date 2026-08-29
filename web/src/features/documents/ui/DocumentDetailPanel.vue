<script setup lang="ts">
import { computed, ref, toRef, watch } from 'vue'
import type { DocumentStatus } from '../../../entities/document/model/document'
import type { ProcessingJobStatus } from '../../../entities/processing-job/model/processing-job'
import { useDocumentDetail } from '../model/use-document-detail'

const props = defineProps<{ documentId: number | null }>()
const emit = defineEmits<{
  close: []
  deleted: [documentId: number]
  updated: []
}>()

const showDeleteConfirmation = ref(false)
const selectedDocumentId = toRef(props, 'documentId')
const {
  canDelete,
  canStartProcessing,
  chunkErrorMessage,
  chunkPage,
  chunkRequestId,
  chunkState,
  deleteErrorMessage,
  deleteRequestId,
  detailErrorMessage,
  detailRequestId,
  detailState,
  document,
  isDeleting,
  isRecoveringUnknownJob,
  load,
  loadChunks,
  processing,
  remove,
  startProcessing,
} = useDocumentDetail(selectedDocumentId)

const statusLabels: Record<DocumentStatus, string> = {
  uploaded: '等待解析',
  processing: '解析中',
  ready: '解析完成',
  failed: '解析失败',
}

const jobStatusLabels: Record<ProcessingJobStatus, string> = {
  queued: '任务已排队',
  processing: '正在解析',
  succeeded: '解析任务完成',
  failed: '解析任务失败',
  canceled: '解析任务已取消',
}

const canGoPrevious = computed(() => (chunkPage.value?.pagination.page ?? 1) > 1)
const canGoNext = computed(
  () =>
    chunkPage.value !== null &&
    chunkPage.value.pagination.page < chunkPage.value.pagination.totalPages,
)
const processingIsEligible = computed(
  () => document.value?.status === 'uploaded' || document.value?.status === 'failed',
)

watch(
  () => document.value?.status,
  (nextStatus, previousStatus) => {
    if (nextStatus && previousStatus && nextStatus !== previousStatus) emit('updated')
  },
)

watch(selectedDocumentId, () => {
  showDeleteConfirmation.value = false
})

function displayTitle(): string {
  return document.value?.title?.trim() || document.value?.originalName || '文档详情'
}

function formatDate(value: Date): string {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(value)
}

function formatFileSize(sizeBytes: number): string {
  if (sizeBytes < 1024) return `${sizeBytes} B`
  if (sizeBytes < 1024 * 1024) return `${(sizeBytes / 1024).toFixed(1)} KiB`
  return `${(sizeBytes / 1024 / 1024).toFixed(1)} MiB`
}

function pageRangeLabel(pageStart: number | null, pageEnd: number | null): string {
  if (pageStart === null || pageEnd === null) return '无固定页码'
  return pageStart === pageEnd ? `第 ${pageStart} 页` : `第 ${pageStart}–${pageEnd} 页`
}

async function handleDelete(): Promise<void> {
  const activeDocumentId = document.value?.id
  if (!activeDocumentId) return
  const succeeded = await remove()
  if (succeeded) emit('deleted', activeDocumentId)
}
</script>

<template>
  <aside v-if="documentId !== null" class="detail-panel" aria-labelledby="document-detail-title">
    <header class="detail-header">
      <div>
        <p>Document detail</p>
        <h2 id="document-detail-title">{{ displayTitle() }}</h2>
      </div>
      <button class="icon-button" type="button" aria-label="关闭文档详情" @click="emit('close')">
        ×
      </button>
    </header>

    <div v-if="detailState === 'loading'" class="detail-state" role="status">
      <strong>正在读取文档详情</strong>
      <p>同时检查当前解析状态。</p>
    </div>

    <div v-else-if="detailState === 'error'" class="detail-state detail-state--error" role="alert">
      <strong>详情加载失败</strong>
      <p>{{ detailErrorMessage }}</p>
      <small v-if="detailRequestId">请求编号：{{ detailRequestId }}</small>
      <button class="secondary-button" type="button" @click="load">重新加载</button>
    </div>

    <template v-else-if="detailState === 'success' && document">
      <section class="document-summary" aria-label="文档摘要">
        <div class="summary-heading">
          <span class="status-badge" :class="`status-badge--${document.status}`">
            {{ statusLabels[document.status] }}
          </span>
          <span>#{{ document.id }}</span>
        </div>
        <dl>
          <div>
            <dt>原始文件</dt>
            <dd>{{ document.originalName }}</dd>
          </div>
          <div>
            <dt>类型 / 大小</dt>
            <dd>{{ document.mimeType }} · {{ formatFileSize(document.sizeBytes) }}</dd>
          </div>
          <div>
            <dt>最近更新</dt>
            <dd>{{ formatDate(document.updatedAt) }}</dd>
          </div>
          <div>
            <dt>内容指纹</dt>
            <dd class="hash-value" :title="document.sha256">{{ document.sha256 }}</dd>
          </div>
        </dl>
      </section>

      <div v-if="document.errorMessage" class="inline-notice inline-notice--error" role="alert">
        <strong>上次解析没有完成</strong>
        <p>{{ document.errorMessage }}</p>
      </div>

      <section class="processing-section" aria-labelledby="processing-title">
        <div class="section-heading">
          <div>
            <p>Processing</p>
            <h3 id="processing-title">解析任务</h3>
          </div>
          <button
            v-if="processingIsEligible"
            class="primary-button"
            type="button"
            :disabled="!canStartProcessing"
            @click="startProcessing"
          >
            {{
              processing.isCoolingDown.value
                ? `${processing.retryAfterSeconds.value} 秒后可重试`
                : document.status === 'failed'
                  ? '重新解析'
                  : '开始解析'
            }}
          </button>
        </div>

        <div v-if="processing.state.value === 'discovering'" class="inline-notice" role="status">
          正在恢复最近解析任务…
        </div>
        <div v-else-if="processing.state.value === 'queueing'" class="inline-notice" role="status">
          正在创建解析任务…
        </div>
        <div
          v-else-if="processing.state.value === 'capacity' && processing.capacityFailure.value"
          class="inline-notice inline-notice--capacity"
          role="alert"
        >
          <strong>{{ processing.capacityFailure.value.title }}</strong>
          <p>{{ processing.capacityFailure.value.message }}</p>
          <p v-if="processing.isCoolingDown.value">
            {{ processing.retryAfterSeconds.value }} 秒后可手动重试。
          </p>
          <small v-if="processing.requestId.value"
            >请求编号：{{ processing.requestId.value }}</small
          >
        </div>
        <div
          v-else-if="processing.state.value === 'conflict' || isRecoveringUnknownJob"
          class="inline-notice"
          role="status"
        >
          <strong>任务状态刚刚变化</strong>
          <p>{{ processing.errorMessage.value }} 正在从后端恢复最新状态。</p>
        </div>
        <div
          v-else-if="processing.state.value === 'error'"
          class="inline-notice inline-notice--error"
        >
          <strong>任务状态读取失败</strong>
          <p>{{ processing.errorMessage.value }}</p>
          <small v-if="processing.requestId.value"
            >请求编号：{{ processing.requestId.value }}</small
          >
          <button
            v-if="processing.hasActiveJob.value"
            class="text-button"
            type="button"
            @click="processing.resumePolling"
          >
            继续轮询
          </button>
        </div>
        <div v-else-if="processing.job.value" class="job-card" role="status">
          <div>
            <div class="job-card__identity">
              <strong>{{ jobStatusLabels[processing.job.value.status] }}</strong>
              <span>任务 #{{ processing.job.value.id }}</span>
            </div>
            <button
              v-if="processing.job.value.cancelable"
              class="text-button text-button--danger"
              type="button"
              :disabled="processing.isCancelling.value"
              @click="processing.cancel"
            >
              {{ processing.isCancelling.value ? '正在取消…' : '取消排队' }}
            </button>
          </div>
          <p v-if="processing.job.value.errorMessage">
            {{ processing.job.value.errorMessage }}
          </p>
          <small>尝试次数：{{ processing.job.value.attemptCount }}</small>
        </div>
        <p v-else-if="document.status === 'ready'" class="section-help">
          文档文本已经解析并切分，可在下方查看；这不代表已经完成向量化。
        </p>
        <p v-else-if="document.status === 'processing'" class="section-help">
          页面正在观察后端处理结果。离开页面不会中断后端任务。
        </p>
        <p v-else class="section-help">解析后才能浏览文本块并进入后续检索流程。</p>
      </section>

      <section class="chunks-section" aria-labelledby="chunks-title">
        <div class="section-heading">
          <div>
            <p>Parsed content</p>
            <h3 id="chunks-title">文本块</h3>
          </div>
          <span v-if="chunkPage">共 {{ chunkPage.pagination.total }} 块</span>
        </div>

        <div v-if="document.status !== 'ready'" class="empty-chunks">
          文档解析完成后，这里会按原文顺序展示文本块。
        </div>
        <div v-else-if="chunkState === 'loading'" class="empty-chunks" role="status">
          正在读取文本块…
        </div>
        <div
          v-else-if="chunkState === 'error'"
          class="inline-notice inline-notice--error"
          role="alert"
        >
          <strong>文本块加载失败</strong>
          <p>{{ chunkErrorMessage }}</p>
          <small v-if="chunkRequestId">请求编号：{{ chunkRequestId }}</small>
          <button class="text-button" type="button" @click="loadChunks()">重新加载</button>
        </div>
        <div v-else-if="chunkState === 'empty'" class="empty-chunks">
          后端返回了空的文本块列表。
        </div>
        <template v-else-if="chunkState === 'success' && chunkPage">
          <ol class="chunk-list">
            <li v-for="chunk in chunkPage.chunks" :key="chunk.id">
              <header>
                <strong>块 {{ chunk.index + 1 }}</strong>
                <span>{{ pageRangeLabel(chunk.pageStart, chunk.pageEnd) }}</span>
              </header>
              <p>{{ chunk.content }}</p>
            </li>
          </ol>
          <nav
            v-if="chunkPage.pagination.totalPages > 1"
            class="chunk-pagination"
            aria-label="文本块分页"
          >
            <button
              class="secondary-button"
              type="button"
              :disabled="!canGoPrevious"
              @click="loadChunks(chunkPage.pagination.page - 1)"
            >
              上一页
            </button>
            <span>{{ chunkPage.pagination.page }} / {{ chunkPage.pagination.totalPages }}</span>
            <button
              class="secondary-button"
              type="button"
              :disabled="!canGoNext"
              @click="loadChunks(chunkPage.pagination.page + 1)"
            >
              下一页
            </button>
          </nav>
        </template>
      </section>

      <section class="danger-section" aria-labelledby="delete-title">
        <div>
          <h3 id="delete-title">删除文档</h3>
          <p>删除会同时移除服务器文件、解析任务和文本块，无法从页面恢复。</p>
        </div>
        <button
          v-if="!showDeleteConfirmation"
          class="danger-button"
          type="button"
          :disabled="!canDelete"
          @click="showDeleteConfirmation = true"
        >
          删除…
        </button>
        <div v-else class="delete-confirmation" role="alert">
          <strong>确认删除“{{ displayTitle() }}”吗？</strong>
          <div>
            <button
              class="secondary-button"
              type="button"
              :disabled="isDeleting"
              @click="showDeleteConfirmation = false"
            >
              取消
            </button>
            <button
              class="danger-button danger-button--solid"
              type="button"
              :disabled="isDeleting"
              @click="handleDelete"
            >
              {{ isDeleting ? '正在删除…' : '确认删除' }}
            </button>
          </div>
        </div>
        <div v-if="deleteErrorMessage" class="inline-notice inline-notice--error" role="alert">
          <p>{{ deleteErrorMessage }}</p>
          <small v-if="deleteRequestId">请求编号：{{ deleteRequestId }}</small>
        </div>
      </section>
    </template>
  </aside>
</template>

<style scoped>
.detail-panel {
  min-width: 0;
  padding: 22px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface);
}

.detail-header,
.section-heading,
.summary-heading,
.job-card > div,
.chunk-list header,
.chunk-pagination,
.danger-section,
.delete-confirmation > div {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 14px;
}

.detail-header {
  align-items: flex-start;
  padding-bottom: 18px;
  border-bottom: 1px solid var(--color-border);
}

.detail-header p,
.section-heading p {
  margin-bottom: 6px;
  color: var(--color-accent);
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.detail-header h2 {
  overflow-wrap: anywhere;
  margin-bottom: 0;
  font-size: 18px;
}

.icon-button {
  display: grid;
  width: 32px;
  height: 32px;
  flex: 0 0 auto;
  place-items: center;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: 22px;
}

.icon-button:hover {
  background: var(--color-surface-hover);
}

.detail-state,
.empty-chunks {
  margin-top: 18px;
  padding: 18px;
  border-radius: 11px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.detail-state--error,
.inline-notice--error {
  background: var(--color-danger-soft);
}

.inline-notice--capacity {
  border: 1px solid var(--color-border-strong);
  background: var(--color-accent-soft);
}

.detail-state p,
.inline-notice p {
  margin: 5px 0 0;
}

.detail-state small,
.inline-notice small {
  display: block;
  margin-top: 6px;
  color: var(--color-text-subtle);
  font-size: 11px;
}

.detail-state .secondary-button {
  margin-top: 12px;
}

.document-summary,
.processing-section,
.chunks-section,
.danger-section {
  margin-top: 22px;
}

.summary-heading {
  justify-content: flex-start;
  color: var(--color-text-subtle);
  font-size: 11px;
}

.status-badge {
  padding: 5px 9px;
  border-radius: 999px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 650;
}

.status-badge--processing {
  background: #eef1f7;
  color: #4c5e7a;
}

.status-badge--ready {
  background: var(--color-accent-soft);
  color: var(--color-accent);
}

.status-badge--failed {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

dl {
  display: grid;
  gap: 12px;
  margin: 16px 0 0;
}

dl > div {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: 12px;
}

dt {
  color: var(--color-text-subtle);
  font-size: 11px;
}

dd {
  min-width: 0;
  margin: 0;
  overflow-wrap: anywhere;
  color: var(--color-text-muted);
  font-size: 12px;
}

.hash-value {
  overflow: hidden;
  font-family: ui-monospace, SFMono-Regular, Consolas, monospace;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.section-heading h3,
.danger-section h3 {
  margin-bottom: 0;
  font-size: 14px;
}

.section-heading > span {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.primary-button,
.secondary-button,
.danger-button {
  min-height: 36px;
  padding: 0 12px;
  border-radius: 8px;
  cursor: pointer;
  font-size: 12px;
  font-weight: 650;
}

.primary-button {
  border: 1px solid var(--color-accent);
  background: var(--color-accent);
  color: white;
}

.secondary-button {
  border: 1px solid var(--color-border-strong);
  background: var(--color-surface);
  color: var(--color-text-strong);
}

.danger-button {
  border: 1px solid #d9aaa3;
  background: transparent;
  color: var(--color-danger);
}

.danger-button--solid {
  background: var(--color-danger);
  color: white;
}

.primary-button:disabled,
.secondary-button:disabled,
.danger-button:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}

.inline-notice,
.job-card {
  margin-top: 12px;
  padding: 12px 14px;
  border-radius: 10px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.55;
}

.job-card strong {
  color: var(--color-text-strong);
}

.job-card__identity {
  display: grid;
  gap: 2px;
}

.job-card span,
.job-card small {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.job-card > p {
  margin: 8px 0;
  color: var(--color-danger);
}

.section-help {
  margin: 12px 0 0;
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.6;
}

.text-button {
  margin-top: 8px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-size: 12px;
  font-weight: 650;
}

.chunk-list {
  display: grid;
  gap: 9px;
  margin: 12px 0 0;
  padding: 0;
  list-style: none;
}

.chunk-list li {
  padding: 13px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-surface-subtle);
}

.chunk-list header strong {
  font-size: 11px;
}

.chunk-list header span {
  color: var(--color-text-subtle);
  font-size: 10px;
}

.chunk-list p {
  margin: 9px 0 0;
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.65;
  overflow-wrap: anywhere;
  white-space: pre-wrap;
}

.chunk-pagination {
  justify-content: center;
  margin-top: 14px;
  color: var(--color-text-muted);
  font-size: 11px;
}

.danger-section {
  align-items: flex-start;
  padding-top: 20px;
  border-top: 1px solid var(--color-border);
}

.danger-section > div:first-child p {
  max-width: 280px;
  margin: 6px 0 0;
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.55;
}

.delete-confirmation {
  display: grid;
  width: 100%;
  gap: 10px;
  padding: 13px;
  border-radius: 10px;
  background: var(--color-danger-soft);
  font-size: 12px;
}

.delete-confirmation > div {
  justify-content: flex-end;
}

.danger-section > .inline-notice {
  width: 100%;
}

@media (max-width: 520px) {
  .detail-panel {
    padding: 18px;
  }

  .danger-section {
    display: grid;
  }

  dl > div {
    grid-template-columns: 1fr;
    gap: 4px;
  }
}
</style>
