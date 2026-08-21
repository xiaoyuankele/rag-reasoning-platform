<script setup lang="ts">
import { computed } from 'vue'
import type { ResearchDocument } from '../../../entities/document/model/document'
import {
  allDocumentsScope,
  type DocumentScope,
} from '../../../entities/document/model/document-scope'
import { useDocumentScopeOptions } from '../model/use-document-scope-options'

const props = withDefaults(
  defineProps<{
    modelValue: DocumentScope
    invalidSelection?: boolean
  }>(),
  {
    invalidSelection: false,
  },
)

const emit = defineEmits<{
  'update:modelValue': [scope: DocumentScope]
}>()

const { documents, errorMessage, isLoading, load, requestId, state } = useDocumentScopeOptions()

const selectedValue = computed(() =>
  props.modelValue.kind === 'single' ? `document:${props.modelValue.documentId}` : 'all',
)

const selectedDocument = computed(() => {
  const scope = props.modelValue
  if (scope.kind !== 'single') return null
  return documents.value.find((document) => document.id === scope.documentId) ?? null
})

const unavailableDocumentId = computed(() => {
  if (
    props.invalidSelection ||
    props.modelValue.kind !== 'single' ||
    !['success', 'empty'].includes(state.value) ||
    selectedDocument.value
  ) {
    return null
  }

  return props.modelValue.documentId
})

const scopeDescription = computed(() => {
  if (props.invalidSelection) return 'URL 中的文档范围无效，请改用全部文档或重新选择。'
  if (props.modelValue.kind === 'all') {
    return '使用当前账户所有已经解析完成的文档。'
  }
  if (selectedDocument.value) {
    return `只使用“${displayTitle(selectedDocument.value)}”。`
  }
  return `文档 #${props.modelValue.documentId} 当前不可用于检索或问答。`
})

function displayTitle(document: ResearchDocument): string {
  return document.title?.trim() || document.originalName
}

function optionLabel(document: ResearchDocument): string {
  const title = displayTitle(document)
  const name = title === document.originalName ? title : `${title} · ${document.originalName}`
  return `${name} · #${document.id}`
}

function handleChange(event: Event): void {
  const value = (event.target as HTMLSelectElement).value
  if (value === 'all') {
    emit('update:modelValue', allDocumentsScope())
    return
  }

  const match = /^document:(\d+)$/.exec(value)
  const documentId = match ? Number(match[1]) : Number.NaN
  if (Number.isSafeInteger(documentId) && documentId > 0) {
    emit('update:modelValue', { kind: 'single', documentId })
  }
}
</script>

<template>
  <section class="scope-picker" aria-labelledby="document-scope-title">
    <header>
      <div>
        <p>Retrieval scope</p>
        <h2 id="document-scope-title">检索范围</h2>
      </div>
      <span v-if="state === 'success'">{{ documents.length }} 份可检索文档</span>
    </header>

    <div class="scope-control">
      <label for="document-scope">文档范围</label>
      <select
        id="document-scope"
        name="document_scope"
        :value="selectedValue"
        :aria-describedby="'document-scope-help'"
        @change="handleChange"
      >
        <option value="all">全部可检索文档</option>
        <option
          v-if="unavailableDocumentId !== null"
          :value="`document:${unavailableDocumentId}`"
          disabled
        >
          文档 #{{ unavailableDocumentId }}（当前不可用）
        </option>
        <optgroup v-if="documents.length > 0" label="指定单篇文档">
          <option
            v-for="document in documents"
            :key="document.id"
            :value="`document:${document.id}`"
          >
            {{ optionLabel(document) }}
          </option>
        </optgroup>
      </select>
      <small id="document-scope-help">{{ scopeDescription }}</small>
    </div>

    <div v-if="isLoading" class="scope-notice" role="status">正在读取可检索文档…</div>
    <div v-else-if="state === 'error'" class="scope-notice scope-notice--error" role="alert">
      <div>
        <strong>单篇文档列表加载失败</strong>
        <p>{{ errorMessage }} “全部文档”范围仍然可用。</p>
        <small v-if="requestId">请求编号：{{ requestId }}</small>
      </div>
      <button type="button" @click="load">重新加载</button>
    </div>
    <div
      v-if="!isLoading && (invalidSelection || unavailableDocumentId !== null)"
      class="scope-notice scope-notice--warning"
      role="alert"
    >
      <span>{{ scopeDescription }}</span>
      <button type="button" @click="emit('update:modelValue', allDocumentsScope())">
        使用全部文档
      </button>
    </div>
    <div v-else-if="!isLoading && state === 'empty' && !invalidSelection" class="scope-notice">
      当前没有解析完成的文档。选择“全部”仍可提交范围，但检索或问答将没有可用证据。
    </div>
  </section>
</template>

<style scoped>
.scope-picker {
  max-width: 920px;
  margin-bottom: 18px;
  padding: 18px 20px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

.scope-picker > header,
.scope-notice {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
}

.scope-picker > header p {
  margin-bottom: 5px;
  color: var(--color-accent);
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.scope-picker > header h2 {
  margin-bottom: 0;
  font-size: 16px;
}

.scope-picker > header > span {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.scope-control {
  display: grid;
  grid-template-columns: 112px minmax(0, 1fr);
  align-items: center;
  gap: 8px 14px;
  margin-top: 16px;
}

.scope-control label {
  color: var(--color-text-strong);
  font-size: 12px;
  font-weight: 650;
}

.scope-control select {
  width: 100%;
  min-width: 0;
  height: 42px;
  padding: 0 36px 0 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  outline: none;
  background: var(--color-surface);
  color: var(--color-text-strong);
}

.scope-control select:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

.scope-control small {
  grid-column: 2;
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.5;
}

.scope-notice {
  margin-top: 12px;
  padding: 10px 12px;
  border-radius: 9px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.5;
}

.scope-notice--error {
  background: var(--color-danger-soft);
}

.scope-notice--warning {
  background: #f7f1e7;
}

.scope-notice p {
  margin: 4px 0 0;
}

.scope-notice small {
  display: block;
  margin-top: 4px;
  color: var(--color-text-subtle);
}

.scope-notice button {
  flex: 0 0 auto;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-size: 11px;
  font-weight: 650;
}

@media (max-width: 620px) {
  .scope-control {
    grid-template-columns: 1fr;
  }

  .scope-control small {
    grid-column: 1;
  }

  .scope-notice {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
