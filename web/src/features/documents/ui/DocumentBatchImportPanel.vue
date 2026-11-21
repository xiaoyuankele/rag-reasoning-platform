<script setup lang="ts">
import { computed, onScopeDispose, ref, watch } from 'vue'
import type { DocumentImportItem, DocumentImportState } from '../model/use-document-import-queue'
import { useDocumentImportQueue } from '../model/use-document-import-queue'

const emit = defineEmits<{
  changed: []
  select: [documentId: number]
}>()

const hasStarted = ref(false)
const {
  addFiles,
  canStart,
  canStop,
  clearFinished,
  hasRetryableItems,
  hasStoppedItems,
  isDispatching,
  items,
  removeItem,
  resumeStopped,
  retryFailed,
  selectionMessage,
  start,
  stopRemaining,
  summary,
} = useDocumentImportQueue()
let changedTimer: ReturnType<typeof setTimeout> | undefined

const settledCount = computed(
  () => summary.value.ready + summary.value.failed + summary.value.stopped,
)
const progressPercent = computed(() =>
  summary.value.total === 0 ? 0 : Math.round((settledCount.value / summary.value.total) * 100),
)

const stateLabels: Record<DocumentImportState, string> = {
  waiting: '等待导入',
  hashing: '本地检查中',
  checking: '正在检查重复',
  duplicate: '已有内容',
  uploading: '正在上传',
  queueing: '正在排队',
  queued: '等待解析',
  processing: '正在解析',
  ready: '解析完成',
  'hash-failed': '本地检查失败',
  'check-failed': '重复预检失败',
  'upload-failed': '上传失败',
  'queue-failed': '排队失败',
  'process-failed': '解析失败',
  stopped: '已停止',
}

function scheduleChanged(): void {
  if (changedTimer !== undefined) clearTimeout(changedTimer)
  changedTimer = setTimeout(() => emit('changed'), 120)
}

watch(isDispatching, (nextValue, previousValue) => {
  if (previousValue && !nextValue) scheduleChanged()
})

watch(
  () => summary.value.active,
  (nextValue, previousValue) => {
    if (hasStarted.value && previousValue > 0 && nextValue === 0) scheduleChanged()
  },
)

onScopeDispose(() => {
  if (changedTimer !== undefined) clearTimeout(changedTimer)
})

function handleFileChange(event: Event): void {
  const target = event.currentTarget as HTMLInputElement
  addFiles(Array.from(target.files ?? []))
  target.value = ''
}

function startImport(): void {
  hasStarted.value = true
  void start()
}

function retryImportFailures(): void {
  hasStarted.value = true
  void retryFailed()
}

function resumeStoppedImports(): void {
  hasStarted.value = true
  void resumeStopped()
}

function formatFileSize(sizeBytes: number): string {
  if (sizeBytes < 1024) return `${sizeBytes} B`
  if (sizeBytes < 1024 * 1024) return `${(sizeBytes / 1024).toFixed(1)} KiB`
  return `${(sizeBytes / 1024 / 1024).toFixed(1)} MiB`
}

function isActive(item: DocumentImportItem): boolean {
  return ['hashing', 'checking', 'uploading', 'queueing', 'queued', 'processing'].includes(
    item.state,
  )
}

function itemStatusLabel(item: DocumentImportItem): string {
  if ((item.state === 'ready' || item.state === 'duplicate') && item.duplicate) {
    return '已有内容 · 可用'
  }
  return stateLabels[item.state]
}

function hashProgressPercent(item: DocumentImportItem): number {
  const progress = item.hashProgress
  if (!progress || progress.totalBytes <= 0) return 0
  return Math.min(100, Math.round((progress.processedBytes / progress.totalBytes) * 100))
}
</script>

<template>
  <section class="batch-import" aria-labelledby="batch-import-title">
    <header class="import-header">
      <div>
        <p>Import documents</p>
        <h2 id="batch-import-title">批量导入并解析</h2>
        <span>单批最多 20 份；先在本地检查内容，后端确认已有时不会重复上传。</span>
      </div>
      <div class="import-actions">
        <label class="file-picker" for="document-files">
          <span>{{ items.length === 0 ? '选择文件' : '继续添加' }}</span>
          <input
            id="document-files"
            name="files"
            type="file"
            multiple
            accept=".pdf,.md,.markdown,.txt,application/pdf,text/markdown,text/plain"
            :disabled="isDispatching"
            @change="handleFileChange"
          />
        </label>
        <button class="primary-button" type="button" :disabled="!canStart" @click="startImport">
          {{ isDispatching ? '正在导入…' : '导入并解析' }}
        </button>
      </div>
    </header>

    <p v-if="selectionMessage" class="selection-message" role="alert">
      {{ selectionMessage }}
    </p>

    <div v-if="items.length === 0" class="import-empty">
      选择 PDF、Markdown 或纯文本文件。单文件仍可使用同一入口导入。
    </div>

    <template v-else>
      <section class="batch-summary" aria-label="导入批次进度" aria-live="polite">
        <div class="summary-copy">
          <strong>共 {{ summary.total }} 份</strong>
          <span v-if="summary.waiting">待开始 {{ summary.waiting }}</span>
          <span>完成 {{ summary.ready }}</span>
          <span v-if="summary.duplicate">已有 {{ summary.duplicate }}</span>
          <span v-if="summary.failed">失败 {{ summary.failed }}</span>
          <span v-if="summary.active">进行中 {{ summary.active }}</span>
        </div>
        <div class="progress-track" aria-hidden="true">
          <span :style="{ width: `${progressPercent}%` }" />
        </div>
        <div class="batch-actions">
          <button
            v-if="canStop"
            class="text-button text-button--danger"
            type="button"
            @click="stopRemaining"
          >
            停止剩余
          </button>
          <button
            v-if="hasRetryableItems"
            class="text-button"
            type="button"
            @click="retryImportFailures"
          >
            重试失败项
          </button>
          <button
            v-if="hasStoppedItems"
            class="text-button"
            type="button"
            @click="resumeStoppedImports"
          >
            继续已停止项
          </button>
          <button
            v-if="settledCount > 0 && !isDispatching"
            class="text-button"
            type="button"
            @click="clearFinished"
          >
            清理已结束项
          </button>
        </div>
      </section>

      <ol class="import-list">
        <li v-for="item in items" :key="item.localId" class="import-item">
          <div class="file-copy">
            <strong :title="item.file.name">{{ item.file.name }}</strong>
            <span>{{ formatFileSize(item.file.size) }}</span>
            <span v-if="item.state === 'hashing'"> 本地检查 {{ hashProgressPercent(item) }}% </span>
            <span
              v-if="
                item.duplicate && item.document && item.document.originalName !== item.file.name
              "
              :title="item.document.originalName"
            >
              已有文件：{{ item.document.originalName }}
            </span>
            <span v-if="item.document">文档 #{{ item.document.id }}</span>
            <span v-if="item.job">任务 #{{ item.job.id }}</span>
          </div>
          <span class="import-status" :class="`import-status--${item.state}`">
            {{ itemStatusLabel(item) }}
          </span>
          <div v-if="item.errorMessage" class="item-error" role="status">
            <span>{{ item.errorMessage }}</span>
            <small v-if="item.requestId">请求编号：{{ item.requestId }}</small>
          </div>
          <div v-if="item.warningMessage" class="item-warning" role="status">
            <span>{{ item.warningMessage }}</span>
            <small v-if="item.warningRequestId">请求编号：{{ item.warningRequestId }}</small>
          </div>
          <div class="item-actions">
            <button
              v-if="item.document"
              class="text-button"
              type="button"
              @click="emit('select', item.document.id)"
            >
              查看文档
            </button>
            <button
              v-if="!isActive(item)"
              class="icon-button"
              type="button"
              :aria-label="`从队列移除 ${item.file.name}`"
              @click="removeItem(item.localId)"
            >
              ×
            </button>
          </div>
        </li>
      </ol>

      <p class="stop-help">
        “停止剩余”会取消本地哈希和尚未完成的浏览器请求；后端已经创建的解析任务仍会继续运行。
      </p>
    </template>
  </section>
</template>

<style scoped>
.batch-import {
  display: grid;
  gap: 16px;
  margin-bottom: 32px;
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface-subtle);
}

.import-header,
.summary-copy,
.batch-actions,
.import-item,
.file-copy,
.item-actions {
  display: flex;
  align-items: center;
}

.import-header {
  justify-content: space-between;
  gap: 24px;
}

.import-header > div:first-child {
  min-width: 0;
}

.import-header p {
  margin-bottom: 7px;
  color: var(--color-accent);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.import-header h2 {
  margin-bottom: 7px;
  font-size: 20px;
  letter-spacing: -0.025em;
}

.import-header > div:first-child > span {
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.import-actions {
  display: flex;
  flex: 0 0 auto;
  gap: 10px;
}

.file-picker {
  position: relative;
  display: grid;
  min-height: 42px;
  padding: 0 14px;
  place-items: center;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  background: var(--color-surface);
  cursor: pointer;
  font-size: 13px;
  font-weight: 650;
}

.file-picker:hover {
  background: var(--color-surface-hover);
}

.file-picker:has(input:disabled) {
  cursor: not-allowed;
  opacity: 0.55;
}

.file-picker input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.primary-button {
  min-height: 42px;
  padding: 0 14px;
  border: 1px solid var(--color-accent);
  border-radius: 9px;
  background: var(--color-accent);
  color: white;
  cursor: pointer;
  font-weight: 650;
}

.primary-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.selection-message,
.item-error {
  color: var(--color-danger);
  font-size: 12px;
}

.item-warning {
  color: var(--color-text-muted);
  font-size: 12px;
}

.selection-message {
  margin: 0;
}

.import-empty {
  padding: 16px;
  border: 1px dashed var(--color-border-strong);
  border-radius: 11px;
  color: var(--color-text-muted);
  font-size: 12px;
}

.batch-summary {
  display: grid;
  gap: 9px;
  padding: 13px 14px;
  border-radius: 11px;
  background: var(--color-surface);
}

.summary-copy {
  flex-wrap: wrap;
  gap: 7px 14px;
  color: var(--color-text-muted);
  font-size: 11px;
}

.summary-copy strong {
  color: var(--color-text-strong);
  font-size: 12px;
}

.progress-track {
  height: 4px;
  overflow: hidden;
  border-radius: 999px;
  background: var(--color-border);
}

.progress-track span {
  display: block;
  height: 100%;
  border-radius: inherit;
  background: var(--color-accent);
  transition: width 180ms ease;
}

.batch-actions {
  flex-wrap: wrap;
  gap: 12px;
}

.text-button {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-size: 11px;
  font-weight: 650;
}

.text-button--danger {
  color: var(--color-danger);
}

.import-list {
  display: grid;
  max-height: 430px;
  gap: 7px;
  margin: 0;
  padding: 0;
  overflow-y: auto;
  list-style: none;
}

.import-item {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto auto;
  gap: 8px 14px;
  padding: 12px 14px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
  background: var(--color-surface);
}

.file-copy {
  min-width: 0;
  gap: 7px 12px;
}

.file-copy strong {
  overflow: hidden;
  min-width: 0;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.file-copy span {
  flex: 0 0 auto;
  color: var(--color-text-subtle);
  font-size: 10px;
}

.import-status {
  padding: 4px 8px;
  border-radius: 999px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 10px;
  font-weight: 650;
  white-space: nowrap;
}

.import-status--uploading,
.import-status--hashing,
.import-status--checking,
.import-status--queueing,
.import-status--queued,
.import-status--processing {
  background: #eef1f7;
  color: #4c5e7a;
}

.import-status--ready,
.import-status--duplicate {
  background: var(--color-accent-soft);
  color: var(--color-accent);
}

.import-status--upload-failed,
.import-status--hash-failed,
.import-status--check-failed,
.import-status--queue-failed,
.import-status--process-failed {
  background: var(--color-danger-soft);
  color: var(--color-danger);
}

.item-error,
.item-warning {
  display: grid;
  grid-column: 1 / -1;
  gap: 3px;
}

.item-error small,
.item-warning small {
  color: var(--color-text-subtle);
  font-size: 10px;
}

.item-actions {
  justify-content: flex-end;
  gap: 10px;
}

.icon-button {
  display: grid;
  width: 26px;
  height: 26px;
  place-items: center;
  border: 0;
  border-radius: 7px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: 18px;
}

.icon-button:hover {
  background: var(--color-surface-hover);
}

.stop-help {
  margin: 0;
  color: var(--color-text-subtle);
  font-size: 10px;
  line-height: 1.55;
}

@media (prefers-reduced-motion: reduce) {
  .progress-track span {
    transition: none;
  }
}

@media (max-width: 760px) {
  .import-header {
    align-items: stretch;
    flex-direction: column;
  }

  .import-actions {
    justify-content: flex-start;
  }

  .import-item {
    grid-template-columns: minmax(0, 1fr) auto;
  }

  .file-copy {
    align-items: flex-start;
    flex-direction: column;
    gap: 4px;
  }

  .item-actions {
    grid-column: 1 / -1;
    justify-content: flex-start;
  }
}

@media (max-width: 520px) {
  .batch-import {
    padding: 18px;
  }

  .import-actions {
    align-items: stretch;
    flex-direction: column;
  }
}
</style>
