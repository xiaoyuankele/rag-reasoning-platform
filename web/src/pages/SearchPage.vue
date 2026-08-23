<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  documentIdFromScope,
  parseDocumentScopeQuery,
  type DocumentScope,
} from '../entities/document/model/document-scope'
import DocumentScopePicker from '../features/documents/ui/DocumentScopePicker.vue'
import KeywordSearchPanel from '../features/search/ui/KeywordSearchPanel.vue'
import SemanticSearchPanel from '../features/search/ui/SemanticSearchPanel.vue'
import PageHeader from '../shared/ui/PageHeader.vue'

const route = useRoute()
const router = useRouter()

const parsedScope = computed(() => parseDocumentScopeQuery(route.query.document_id))
const searchPageMode = computed<'keyword' | 'semantic'>(() =>
  route.query.mode === 'semantic' ? 'semantic' : 'keyword',
)
const pageHeading = computed(() =>
  searchPageMode.value === 'semantic'
    ? {
        eyebrow: 'Semantic Retrieval',
        title: '语义检索',
        description: '使用远程向量模型按含义查找相关文本块，并保留来源、页码和相似度。',
      }
    : {
        eyebrow: 'Keyword Retrieval',
        title: '基础检索',
        description: '使用完整短语或 2～8 个关键词查找同一文本块，并保留文档、页码和原始内容。',
      },
)

function updateMode(mode: 'keyword' | 'semantic'): void {
  void router.push({
    name: 'search',
    query: {
      mode: mode === 'semantic' ? 'semantic' : undefined,
      document_id: documentIdFromScope(parsedScope.value.scope)?.toString(),
    },
  })
}

function updateScope(scope: DocumentScope): void {
  void router.push({
    name: 'search',
    query: {
      ...route.query,
      document_id: documentIdFromScope(scope)?.toString(),
      page: undefined,
    },
  })
}
</script>

<template>
  <div class="search-page">
    <PageHeader
      :eyebrow="pageHeading.eyebrow"
      :title="pageHeading.title"
      :description="pageHeading.description"
    />
    <nav class="retrieval-mode-selector" aria-label="检索类型">
      <button
        type="button"
        :class="{ 'retrieval-mode-selector--active': searchPageMode === 'keyword' }"
        :aria-pressed="searchPageMode === 'keyword'"
        @click="updateMode('keyword')"
      >
        <strong>关键词检索</strong>
        <span>免费、精确匹配</span>
      </button>
      <button
        type="button"
        :class="{ 'retrieval-mode-selector--active': searchPageMode === 'semantic' }"
        :aria-pressed="searchPageMode === 'semantic'"
        @click="updateMode('semantic')"
      >
        <strong>语义检索</strong>
        <span>远程模型、按含义匹配</span>
      </button>
    </nav>
    <DocumentScopePicker
      :model-value="parsedScope.scope"
      :invalid-selection="!parsedScope.isValid"
      @update:model-value="updateScope"
    />
    <SemanticSearchPanel
      v-if="searchPageMode === 'semantic'"
      :scope="parsedScope.scope"
      :scope-is-valid="parsedScope.isValid"
    />
    <KeywordSearchPanel v-else />
  </div>
</template>

<style scoped>
.search-page {
  max-width: 1080px;
  margin: 0 auto;
}

.retrieval-mode-selector {
  display: inline-flex;
  gap: 4px;
  margin-bottom: 16px;
  padding: 4px;
  border: 1px solid var(--color-border);
  border-radius: 12px;
  background: var(--color-surface-subtle);
}

.retrieval-mode-selector button {
  display: grid;
  gap: 2px;
  min-width: 170px;
  padding: 9px 13px;
  border: 0;
  border-radius: 8px;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  text-align: left;
}

.retrieval-mode-selector button:hover,
.retrieval-mode-selector--active {
  background: var(--color-surface) !important;
  color: var(--color-text-strong) !important;
  box-shadow: 0 1px 3px rgb(30 36 42 / 8%);
}

.retrieval-mode-selector strong {
  font-size: 13px;
}

.retrieval-mode-selector span {
  color: var(--color-text-subtle);
  font-size: 10px;
}

@media (max-width: 520px) {
  .retrieval-mode-selector {
    display: grid;
    width: 100%;
  }

  .retrieval-mode-selector button {
    min-width: 0;
  }
}
</style>
