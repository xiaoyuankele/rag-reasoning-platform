<script setup lang="ts">
import { computed, ref, shallowRef } from 'vue'
import type { DocumentStatus, ResearchDocument } from '../../../entities/document/model/document'
import { useDocumentLibrary } from '../model/use-document-library'

const fileInput = ref<HTMLInputElement | null>(null)
const selectedFile = shallowRef<File | null>(null)
const formError = ref('')
const {
  isListLoading,
  isUploading,
  listErrorMessage,
  listState,
  loadPage,
  pageData,
  uploadErrorMessage,
  uploadFile,
  uploadNotice,
  uploadRequestId,
} = useDocumentLibrary()

const canGoPrevious = computed(() => pageData.value !== null && pageData.value.pagination.page > 1)
const canGoNext = computed(
  () =>
    pageData.value !== null &&
    pageData.value.pagination.page < pageData.value.pagination.totalPages,
)

const statusLabels: Record<DocumentStatus, string> = {
  uploaded: '等待解析',
  processing: '解析中',
  ready: '可检索',
  failed: '处理失败',
}

function handleFileChange(event: Event): void {
  const target = event.currentTarget as HTMLInputElement
  selectedFile.value = target.files?.[0] ?? null
  formError.value = ''
}

async function handleUpload(): Promise<void> {
  const file = selectedFile.value
  if (!file) {
    formError.value = '请先选择一个文件。'
    return
  }

  formError.value = ''
  const succeeded = await uploadFile(file)
  if (!succeeded) return

  selectedFile.value = null
  if (fileInput.value) fileInput.value.value = ''
}

function displayTitle(document: ResearchDocument): string {
  return document.title?.trim() || document.originalName
}

function fileTypeLabel(mimeType: string): string {
  if (mimeType === 'application/pdf') return 'PDF'
  if (mimeType === 'text/markdown') return 'Markdown'
  if (mimeType === 'text/plain') return '纯文本'
  return mimeType
}

function formatFileSize(sizeBytes: number): string {
  if (sizeBytes < 1024) return `${sizeBytes} B`
  if (sizeBytes < 1024 * 1024) return `${(sizeBytes / 1024).toFixed(1)} KiB`
  return `${(sizeBytes / 1024 / 1024).toFixed(1)} MiB`
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

function goToPage(page: number): void {
  if (page < 1 || isListLoading.value) return
  void loadPage(page)
}
</script>

<template>
  <section class="document-library" aria-labelledby="upload-title">
    <form class="upload-card" @submit.prevent="handleUpload">
      <div class="upload-copy">
        <p>添加资料</p>
        <h2 id="upload-title">上传一个文档</h2>
        <span>支持 PDF、Markdown 和纯文本；内容是否重复由后端安全判定。</span>
      </div>

      <div class="upload-actions">
        <label class="file-picker" for="document-file">
          <span>{{ selectedFile ? '重新选择' : '选择文件' }}</span>
          <input
            id="document-file"
            ref="fileInput"
            name="file"
            type="file"
            accept=".pdf,.md,.markdown,.txt,application/pdf,text/markdown,text/plain"
            :disabled="isUploading"
            aria-describedby="file-selection-help"
            @change="handleFileChange"
          />
        </label>
        <button class="primary-button" type="submit" :disabled="isUploading">
          {{ isUploading ? '正在上传…' : '上传文档' }}
        </button>
      </div>

      <p id="file-selection-help" class="file-selection">
        <template v-if="selectedFile">
          已选择：<strong>{{ selectedFile.name }}</strong>
          <span>· {{ formatFileSize(selectedFile.size) }}</span>
        </template>
        <template v-else>单次上传一个文件，服务端默认上限为 200 MiB。</template>
      </p>

      <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
      <div v-if="uploadErrorMessage" class="upload-notice upload-notice--error" role="alert">
        <strong>上传没有完成</strong>
        <p>{{ uploadErrorMessage }}</p>
        <small v-if="uploadRequestId">请求编号：{{ uploadRequestId }}</small>
      </div>

      <div
        v-else-if="uploadNotice"
        class="upload-notice"
        :class="{ 'upload-notice--duplicate': uploadNotice.duplicate }"
        role="status"
      >
        <strong>{{ uploadNotice.duplicate ? '该内容已存在' : '上传完成' }}</strong>
        <p v-if="uploadNotice.duplicate">
          “{{ uploadNotice.selectedFileName }}”与已有文档“{{
            uploadNotice.document.originalName
          }}”内容完全相同，已保留原记录 #{{ uploadNotice.document.id }}。
        </p>
        <p v-else>“{{ uploadNotice.document.originalName }}”已加入文档库，当前状态为“等待解析”。</p>
      </div>
    </form>

    <section class="library-section" aria-labelledby="library-title">
      <header class="library-header">
        <div>
          <p>我的资料</p>
          <h2 id="library-title">文档列表</h2>
        </div>
        <div class="library-summary">
          <span v-if="pageData">共 {{ pageData.pagination.total }} 份</span>
          <button
            class="text-button"
            type="button"
            :disabled="isListLoading"
            @click="loadPage(pageData?.pagination.page ?? 1)"
          >
            {{ isListLoading ? '刷新中…' : '刷新' }}
          </button>
        </div>
      </header>

      <div v-if="listState === 'loading'" class="state-card" role="status">
        <span class="loading-dot" aria-hidden="true" />
        <div>
          <strong>正在读取文档</strong>
          <p>只会显示当前登录用户拥有的记录。</p>
        </div>
      </div>

      <div v-else-if="listState === 'error'" class="state-card state-card--error" role="alert">
        <div>
          <strong>文档列表加载失败</strong>
          <p>{{ listErrorMessage }}</p>
        </div>
        <button class="secondary-button" type="button" @click="loadPage()">重新加载</button>
      </div>

      <div v-else-if="listState === 'empty'" class="state-card state-card--quiet">
        <strong>文档库还是空的</strong>
        <p>从上方选择一份资料上传。文件成功保存后会出现在这里。</p>
      </div>

      <template v-else-if="pageData">
        <ol class="document-list">
          <li
            v-for="document in pageData.documents"
            :key="document.id"
            class="document-row"
            :class="{ 'document-row--highlighted': uploadNotice?.document.id === document.id }"
          >
            <span class="file-mark" aria-hidden="true">{{ fileTypeLabel(document.mimeType) }}</span>
            <div class="document-main">
              <h3>{{ displayTitle(document) }}</h3>
              <p v-if="document.title">{{ document.originalName }}</p>
              <div class="document-meta">
                <span>#{{ document.id }}</span>
                <span>{{ formatFileSize(document.sizeBytes) }}</span>
                <span>{{ formatDate(document.createdAt) }}</span>
              </div>
            </div>
            <span class="status-badge" :class="`status-badge--${document.status}`">
              {{ statusLabels[document.status] }}
            </span>
          </li>
        </ol>

        <nav v-if="pageData.pagination.totalPages > 1" class="pagination" aria-label="文档列表分页">
          <button
            class="secondary-button"
            type="button"
            :disabled="!canGoPrevious || isListLoading"
            @click="goToPage(pageData.pagination.page - 1)"
          >
            上一页
          </button>
          <span>第 {{ pageData.pagination.page }} / {{ pageData.pagination.totalPages }} 页</span>
          <button
            class="secondary-button"
            type="button"
            :disabled="!canGoNext || isListLoading"
            @click="goToPage(pageData.pagination.page + 1)"
          >
            下一页
          </button>
        </nav>
      </template>
    </section>
  </section>
</template>

<style scoped>
.document-library {
  display: grid;
  max-width: 980px;
  gap: 38px;
}

.upload-card {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 18px 28px;
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 16px;
  background: var(--color-surface-subtle);
}

.upload-copy > p,
.library-header p {
  margin-bottom: 7px;
  color: var(--color-accent);
  font-size: 11px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.upload-copy h2,
.library-header h2 {
  margin-bottom: 7px;
  font-size: 20px;
  letter-spacing: -0.025em;
}

.upload-copy > span {
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.upload-actions {
  display: flex;
  align-items: center;
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

.file-picker input {
  position: absolute;
  width: 1px;
  height: 1px;
  opacity: 0;
}

.primary-button,
.secondary-button {
  min-height: 40px;
  padding: 0 14px;
  border-radius: 9px;
  cursor: pointer;
  font-weight: 650;
}

.primary-button {
  min-height: 42px;
  border: 1px solid var(--color-accent);
  background: var(--color-accent);
  color: white;
}

.secondary-button {
  border: 1px solid var(--color-border-strong);
  background: var(--color-surface);
  color: var(--color-text-strong);
  font-size: 13px;
}

.primary-button:hover:not(:disabled),
.secondary-button:hover:not(:disabled) {
  filter: brightness(0.96);
}

.primary-button:disabled,
.secondary-button:disabled,
.text-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.file-selection,
.form-error,
.upload-notice {
  grid-column: 1 / -1;
}

.file-selection {
  margin: 0;
  color: var(--color-text-muted);
  font-size: 12px;
}

.file-selection strong {
  color: var(--color-text-strong);
  font-weight: 600;
}

.form-error {
  margin: -8px 0 0;
  color: var(--color-danger);
  font-size: 13px;
}

.upload-notice {
  padding: 14px 16px;
  border: 1px solid #c9ded3;
  border-radius: 11px;
  background: var(--color-accent-soft);
}

.upload-notice--duplicate {
  border-color: #ded5ba;
  background: #f7f2e4;
}

.upload-notice--error {
  border-color: #ead0cc;
  background: var(--color-danger-soft);
}

.upload-notice strong {
  font-size: 13px;
}

.upload-notice p {
  margin: 5px 0 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.55;
}

.upload-notice small {
  display: block;
  margin-top: 6px;
  color: var(--color-text-subtle);
  font-size: 11px;
}

.library-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 20px;
  margin-bottom: 14px;
}

.library-header h2 {
  margin-bottom: 0;
}

.library-summary {
  display: flex;
  align-items: center;
  gap: 14px;
  color: var(--color-text-muted);
  font-size: 12px;
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
  display: block;
  border-style: dashed;
  background: var(--color-surface-subtle);
}

.state-card--error {
  border-color: #ead0cc;
  background: var(--color-danger-soft);
}

.state-card strong {
  font-size: 14px;
}

.state-card p {
  margin: 6px 0 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.state-card:has(.loading-dot) {
  justify-content: flex-start;
}

.loading-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 5px var(--color-accent-soft);
  animation: pulse 1.1s ease-in-out infinite alternate;
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
  grid-template-columns: 54px minmax(0, 1fr) auto;
  align-items: center;
  gap: 16px;
  padding: 16px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface);
  transition: border-color 150ms ease;
}

.document-row--highlighted {
  border-color: #9fc4b3;
}

.file-mark {
  display: grid;
  width: 54px;
  height: 42px;
  place-items: center;
  border-radius: 8px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.04em;
}

.document-main {
  min-width: 0;
}

.document-main h3 {
  overflow: hidden;
  margin-bottom: 4px;
  font-size: 14px;
  font-weight: 650;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.document-main > p {
  overflow: hidden;
  margin-bottom: 7px;
  color: var(--color-text-muted);
  font-size: 12px;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.document-meta {
  display: flex;
  flex-wrap: wrap;
  gap: 6px 14px;
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
  white-space: nowrap;
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

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
  margin-top: 20px;
}

.pagination span {
  min-width: 105px;
  color: var(--color-text-muted);
  font-size: 13px;
  text-align: center;
}

@keyframes pulse {
  from {
    opacity: 0.45;
    transform: scale(0.88);
  }
  to {
    opacity: 1;
    transform: scale(1);
  }
}

@media (prefers-reduced-motion: reduce) {
  .loading-dot {
    animation: none;
  }
}

@media (max-width: 760px) {
  .upload-card {
    grid-template-columns: 1fr;
  }

  .upload-actions {
    justify-content: flex-start;
  }

  .file-selection,
  .form-error,
  .upload-notice {
    grid-column: 1;
  }
}

@media (max-width: 520px) {
  .upload-actions,
  .library-header,
  .state-card {
    align-items: stretch;
    flex-direction: column;
  }

  .document-row {
    grid-template-columns: 44px minmax(0, 1fr);
  }

  .file-mark {
    width: 44px;
  }

  .status-badge {
    width: fit-content;
    grid-column: 2;
  }
}
</style>
