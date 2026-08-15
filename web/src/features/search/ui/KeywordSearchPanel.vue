<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryValue } from 'vue-router'
import { useKeywordSearch } from '../model/use-keyword-search'
import KeywordSearchResultCard from './KeywordSearchResultCard.vue'

const pageSize = 10
const route = useRoute()
const router = useRouter()
const queryInput = ref('')
const documentIdInput = ref('')
const formError = ref('')
const { errorMessage, isLoading, reset, resultPage, search, state } = useKeywordSearch()

const canGoPrevious = computed(
  () => resultPage.value !== null && resultPage.value.pagination.page > 1,
)
const canGoNext = computed(
  () =>
    resultPage.value !== null &&
    resultPage.value.pagination.page < resultPage.value.pagination.totalPages,
)

function readQueryValue(value: LocationQueryValue | LocationQueryValue[]): string {
  const firstValue = Array.isArray(value) ? value[0] : value
  return firstValue ?? ''
}

function readPositiveInteger(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null

  const parsed = Number(value)
  return Number.isSafeInteger(parsed) ? parsed : null
}

function currentPageFromRoute(): number {
  return readPositiveInteger(readQueryValue(route.query.page)) ?? 1
}

function validateForm(): { query: string; documentId?: number } | null {
  const query = queryInput.value.trim()
  if (!query) {
    formError.value = '请输入要检索的关键词。'
    return null
  }
  if ([...query].length > 200) {
    formError.value = '关键词不能超过 200 个字符。'
    return null
  }

  const rawDocumentId = documentIdInput.value.trim()
  if (!rawDocumentId) {
    formError.value = ''
    return { query }
  }

  const documentId = readPositiveInteger(rawDocumentId)
  if (documentId === null) {
    formError.value = '文档 ID 必须是正整数。'
    return null
  }

  formError.value = ''
  return { query, documentId }
}

function runSearchFromRoute(): void {
  queryInput.value = readQueryValue(route.query.q)
  documentIdInput.value = readQueryValue(route.query.document_id)
  formError.value = ''

  const query = queryInput.value.trim()
  if (!query) {
    reset()
    return
  }

  const rawDocumentId = documentIdInput.value.trim()
  let documentId: number | undefined
  if (rawDocumentId) {
    const parsedDocumentId = readPositiveInteger(rawDocumentId)
    if (parsedDocumentId === null) {
      formError.value = 'URL 中的文档 ID 无效，请重新输入。'
      reset()
      return
    }
    documentId = parsedDocumentId
  }

  void search({
    query,
    documentId,
    page: currentPageFromRoute(),
    pageSize,
  })
}

function submitSearch(): void {
  const criteria = validateForm()
  if (!criteria) return

  const target = router.resolve({
    name: 'search',
    query: {
      q: criteria.query,
      document_id: criteria.documentId?.toString(),
    },
  })

  if (target.fullPath === route.fullPath) {
    runSearchFromRoute()
    return
  }

  void router.push(target)
}

function goToPage(page: number): void {
  if (page < 1) return

  void router.push({
    name: 'search',
    query: {
      q: readQueryValue(route.query.q),
      document_id: readQueryValue(route.query.document_id) || undefined,
      page: page > 1 ? page.toString() : undefined,
    },
  })
}

watch(() => [route.query.q, route.query.document_id, route.query.page], runSearchFromRoute, {
  immediate: true,
})
</script>

<template>
  <section class="search-panel" aria-labelledby="keyword-search-title">
    <form class="search-form" @submit.prevent="submitSearch">
      <div class="query-field">
        <label id="keyword-search-title" for="keyword-query">关键词</label>
        <input
          id="keyword-query"
          v-model="queryInput"
          name="q"
          type="search"
          autocomplete="off"
          placeholder="例如：磁悬浮振动"
          aria-describedby="query-help"
        />
        <small id="query-help">使用后端字面匹配，不调用远程模型。</small>
      </div>

      <div class="document-field">
        <label for="document-id">文档 ID <span>可选</span></label>
        <input
          id="document-id"
          v-model="documentIdInput"
          name="document_id"
          type="text"
          inputmode="numeric"
          placeholder="全部文档"
        />
      </div>

      <button class="primary-button" type="submit" :disabled="isLoading">
        {{ isLoading ? '检索中' : '开始检索' }}
      </button>
    </form>

    <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>

    <div v-if="state === 'idle'" class="state-card state-card--quiet">
      <strong>从已完成解析的文档中查找内容</strong>
      <p>检索结果以文本块为单位，保留来源文档和页码信息。</p>
    </div>

    <div v-else-if="state === 'loading'" class="state-card" role="status">
      <span class="loading-dot" aria-hidden="true" />
      <div>
        <strong>正在检索</strong>
        <p>正在查询 PostgreSQL 中已就绪的文本块。</p>
      </div>
    </div>

    <div v-else-if="state === 'error'" class="state-card state-card--error" role="alert">
      <div>
        <strong>检索没有完成</strong>
        <p>{{ errorMessage }}</p>
      </div>
      <button type="button" class="secondary-button" @click="runSearchFromRoute">重新检索</button>
    </div>

    <div v-else-if="state === 'empty'" class="state-card state-card--quiet">
      <strong>没有找到匹配的文本块</strong>
      <p>可以缩短关键词、检查文档是否已经解析完成，或移除文档 ID 后搜索全部资料。</p>
    </div>

    <div v-else-if="state === 'success' && resultPage" class="search-results">
      <header class="results-header">
        <div>
          <p>检索结果</p>
          <h2>“{{ resultPage.query }}”</h2>
        </div>
        <span>共 {{ resultPage.pagination.total }} 个文本块</span>
      </header>

      <ol>
        <li v-for="hit in resultPage.results" :key="hit.chunkId">
          <KeywordSearchResultCard :hit="hit" />
        </li>
      </ol>

      <nav v-if="resultPage.pagination.totalPages > 1" class="pagination" aria-label="检索结果分页">
        <button
          type="button"
          class="secondary-button"
          :disabled="!canGoPrevious || isLoading"
          @click="goToPage(resultPage.pagination.page - 1)"
        >
          上一页
        </button>
        <span>
          第 {{ resultPage.pagination.page }} / {{ resultPage.pagination.totalPages }} 页
        </span>
        <button
          type="button"
          class="secondary-button"
          :disabled="!canGoNext || isLoading"
          @click="goToPage(resultPage.pagination.page + 1)"
        >
          下一页
        </button>
      </nav>
    </div>
  </section>
</template>

<style scoped>
.search-panel {
  max-width: 920px;
}

.search-form {
  display: grid;
  grid-template-columns: minmax(0, 1fr) minmax(150px, 210px) auto;
  align-items: end;
  gap: 14px;
  padding: 20px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface-subtle);
}

.query-field,
.document-field {
  display: grid;
  gap: 8px;
}

label {
  color: var(--color-text-strong);
  font-size: 13px;
  font-weight: 650;
}

label span,
small {
  color: var(--color-text-subtle);
  font-size: 11px;
  font-weight: 400;
}

input {
  width: 100%;
  height: 42px;
  padding: 0 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  outline: none;
  background: var(--color-surface);
  color: var(--color-text-strong);
  transition:
    border-color 150ms ease,
    box-shadow 150ms ease;
}

input:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

input::placeholder {
  color: var(--color-text-subtle);
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
  height: 42px;
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
.secondary-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.form-error {
  margin: 10px 4px 0;
  color: var(--color-danger);
  font-size: 13px;
}

.state-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  min-height: 112px;
  margin-top: 22px;
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

.loading-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 5px var(--color-accent-soft);
  animation: pulse 1.1s ease-in-out infinite alternate;
}

.state-card:has(.loading-dot) {
  justify-content: flex-start;
}

.search-results {
  margin-top: 30px;
}

.results-header {
  display: flex;
  align-items: flex-end;
  justify-content: space-between;
  gap: 16px;
  margin-bottom: 16px;
}

.results-header p {
  margin-bottom: 7px;
  color: var(--color-text-subtle);
  font-size: 12px;
  font-weight: 650;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.results-header h2 {
  margin-bottom: 0;
  font-size: 21px;
  letter-spacing: -0.025em;
}

.results-header > span {
  color: var(--color-text-muted);
  font-size: 13px;
}

ol {
  display: grid;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

.pagination {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 16px;
  margin-top: 22px;
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
  .search-form {
    grid-template-columns: 1fr;
  }

  .primary-button {
    justify-self: start;
  }
}

@media (max-width: 520px) {
  .results-header,
  .state-card {
    align-items: flex-start;
    flex-direction: column;
  }

  .pagination {
    gap: 10px;
  }
}
</style>
