<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useRoute, useRouter, type LocationQueryValue } from 'vue-router'
import {
  documentIdFromScope,
  parseDocumentScopeQuery,
} from '../../../entities/document/model/document-scope'
import type { KeywordSearchOperator } from '../../../entities/search-result/model/search-result'
import { useKeywordSearch } from '../model/use-keyword-search'
import KeywordSearchResultCard from './KeywordSearchResultCard.vue'

const pageSize = 10
const maximumTermCount = 8
const route = useRoute()
const router = useRouter()
const searchMode = ref<'phrase' | 'terms'>('phrase')
const queryInput = ref('')
const termInput = ref('')
const termsInput = ref<string[]>([])
const operatorInput = ref<KeywordSearchOperator>('all')
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
const resultKeywords = computed(() => {
  if (!resultPage.value) return []
  return resultPage.value.terms.length > 0 ? resultPage.value.terms : [resultPage.value.query]
})
const resultTitle = computed(() =>
  resultKeywords.value.map((keyword) => `“${keyword}”`).join(' + '),
)
const resultModeDescription = computed(() => {
  if (!resultPage.value || resultPage.value.terms.length === 0) return '连续完整短语'
  return resultPage.value.operator === 'any'
    ? '同一文本块包含任意关键词'
    : '同一文本块同时包含全部关键词'
})

function readQueryValue(value: LocationQueryValue | LocationQueryValue[]): string {
  const firstValue = Array.isArray(value) ? value[0] : value
  return firstValue ?? ''
}

function readQueryValues(value: LocationQueryValue | LocationQueryValue[]): string[] {
  const values = Array.isArray(value) ? value : [value]
  return values.filter((item): item is string => typeof item === 'string')
}

function readPositiveInteger(value: string): number | null {
  if (!/^[1-9]\d*$/.test(value)) return null

  const parsed = Number(value)
  return Number.isSafeInteger(parsed) ? parsed : null
}

function currentPageFromRoute(): number {
  return readPositiveInteger(readQueryValue(route.query.page)) ?? 1
}

function validatePhrase(): string | null {
  const query = queryInput.value.trim()
  if (!query) {
    formError.value = '请输入要检索的关键词。'
    return null
  }
  if ([...query].length > 200) {
    formError.value = '关键词不能超过 200 个字符。'
    return null
  }

  formError.value = ''
  return query
}

function normalizeTerms(rawTerms: string[]): string[] {
  const seenTerms = new Set<string>()
  const terms: string[] = []
  for (const rawTerm of rawTerms) {
    const term = rawTerm.trim()
    if (!term) continue
    const key = term.toLocaleLowerCase()
    if (seenTerms.has(key)) continue
    seenTerms.add(key)
    terms.push(term)
  }
  return terms
}

function validateTerms(rawTerms: string[]): string[] | null {
  const terms = normalizeTerms(rawTerms)
  if (terms.length < 2 || terms.length > maximumTermCount) {
    formError.value = `多关键词检索需要 2～${maximumTermCount} 个不同关键词。`
    return null
  }
  if (terms.some((term) => [...term].length > 100)) {
    formError.value = '每个关键词不能超过 100 个字符。'
    return null
  }
  if (terms.reduce((total, term) => total + [...term].length, 0) > 200) {
    formError.value = '全部关键词合计不能超过 200 个字符。'
    return null
  }

  formError.value = ''
  return terms
}

function addPendingTerms(): boolean {
  const rawValue = termInput.value
  if (!rawValue.trim()) return true

  const candidates = rawValue.split(/[,，\n]+/)
  const nextTerms = normalizeTerms([...termsInput.value, ...candidates])
  if (nextTerms.length > maximumTermCount) {
    formError.value = `最多添加 ${maximumTermCount} 个关键词。`
    return false
  }
  if (nextTerms.some((term) => [...term].length > 100)) {
    formError.value = '每个关键词不能超过 100 个字符。'
    return false
  }

  termsInput.value = nextTerms
  termInput.value = ''
  formError.value = ''
  return true
}

function handleTermKeydown(event: KeyboardEvent): void {
  if (event.key !== 'Enter' && event.key !== ',' && event.key !== '，') return
  event.preventDefault()
  addPendingTerms()
}

function removeTerm(index: number): void {
  termsInput.value = termsInput.value.filter((_, termIndex) => termIndex !== index)
  formError.value = ''
}

function runSearchFromRoute(): void {
  const rawQuery = readQueryValue(route.query.q)
  const rawTerms = readQueryValues(route.query.term)
  const queryProvided = route.query.q !== undefined
  const termsProvided = route.query.term !== undefined
  queryInput.value = rawQuery
  termInput.value = ''
  formError.value = ''

  if (queryProvided && termsProvided) {
    formError.value = 'URL 不能同时包含完整短语 q 和多关键词 term。'
    reset()
    return
  }

  if (!queryProvided && !termsProvided) {
    termsInput.value = []
    reset()
    return
  }

  const parsedScope = parseDocumentScopeQuery(route.query.document_id)
  if (!parsedScope.isValid) {
    formError.value = 'URL 中的文档范围无效，请重新选择。'
    reset()
    return
  }

  if (termsProvided) {
    searchMode.value = 'terms'
    termsInput.value = normalizeTerms(rawTerms)
    const terms = validateTerms(rawTerms)
    if (!terms) {
      reset()
      return
    }
    const rawOperator = readQueryValue(route.query.operator) || 'all'
    const rawWithin = readQueryValue(route.query.within) || 'chunk'
    if (rawOperator !== 'all' && rawOperator !== 'any') {
      formError.value = 'URL 中的关键词组合方式无效。'
      reset()
      return
    }
    if (rawWithin !== 'chunk') {
      formError.value = '当前只支持在同一文本块内检索。'
      reset()
      return
    }
    operatorInput.value = rawOperator
    void search({
      mode: 'terms',
      terms,
      operator: rawOperator,
      within: 'chunk',
      documentId: documentIdFromScope(parsedScope.scope),
      page: currentPageFromRoute(),
      pageSize,
    })
    return
  }

  searchMode.value = 'phrase'
  termsInput.value = []
  const query = validatePhrase()
  if (!query) {
    reset()
    return
  }
  void search({
    mode: 'phrase',
    query,
    documentId: documentIdFromScope(parsedScope.scope),
    page: currentPageFromRoute(),
    pageSize,
  })
}

function submitSearch(): void {
  const parsedScope = parseDocumentScopeQuery(route.query.document_id)
  if (!parsedScope.isValid) {
    formError.value = '请先选择有效的文档范围。'
    return
  }

  let searchQuery: Record<string, string | string[] | undefined>
  if (searchMode.value === 'terms') {
    if (!addPendingTerms()) return
    const terms = validateTerms(termsInput.value)
    if (!terms) return
    termsInput.value = terms
    searchQuery = {
      q: undefined,
      term: terms,
      operator: operatorInput.value,
      within: 'chunk',
    }
  } else {
    const query = validatePhrase()
    if (!query) return
    searchQuery = {
      q: query,
      term: undefined,
      operator: undefined,
      within: undefined,
    }
  }

  const target = router.resolve({
    name: 'search',
    query: {
      ...searchQuery,
      document_id: documentIdFromScope(parsedScope.scope)?.toString(),
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

  const routeTerms = readQueryValues(route.query.term)

  void router.push({
    name: 'search',
    query: {
      q: readQueryValue(route.query.q) || undefined,
      term: routeTerms.length > 0 ? routeTerms : undefined,
      operator: readQueryValue(route.query.operator) || undefined,
      within: readQueryValue(route.query.within) || undefined,
      document_id: readQueryValue(route.query.document_id) || undefined,
      page: page > 1 ? page.toString() : undefined,
    },
  })
}

watch(
  () => [
    route.query.q,
    route.query.term,
    route.query.operator,
    route.query.within,
    route.query.document_id,
    route.query.page,
  ],
  runSearchFromRoute,
  { immediate: true },
)
</script>

<template>
  <section class="search-panel" aria-labelledby="keyword-search-title">
    <form class="search-form" @submit.prevent="submitSearch">
      <fieldset class="search-mode-selector">
        <legend>检索方式</legend>
        <label>
          <input v-model="searchMode" type="radio" value="phrase" />
          完整短语
        </label>
        <label>
          <input v-model="searchMode" type="radio" value="terms" />
          多个关键词
        </label>
      </fieldset>

      <div v-if="searchMode === 'phrase'" class="query-field">
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
        <small id="query-help">按连续完整短语匹配，不自动拆词，不调用远程模型。</small>
      </div>

      <div v-else class="query-field term-field">
        <label id="keyword-search-title" for="keyword-term-input">关键词列表</label>
        <div v-if="termsInput.length > 0" class="term-chips" aria-label="已添加关键词">
          <span v-for="(term, index) in termsInput" :key="`${term}-${index}`">
            {{ term }}
            <button type="button" :aria-label="`移除关键词 ${term}`" @click="removeTerm(index)">
              ×
            </button>
          </span>
        </div>
        <div class="term-entry">
          <input
            id="keyword-term-input"
            v-model="termInput"
            type="text"
            autocomplete="off"
            placeholder="输入关键词后按 Enter，例如：磁悬浮"
            aria-describedby="term-help"
            @keydown="handleTermKeydown"
          />
          <button class="secondary-button" type="button" @click="addPendingTerms">添加</button>
        </div>
        <small id="term-help">
          添加 2～8 个不同关键词；逗号可一次分隔多个。第一版只判断同一文本块，不代表同一句或同一段。
        </small>
      </div>

      <label v-if="searchMode === 'terms'" class="operator-field">
        <span>组合方式</span>
        <select v-model="operatorInput">
          <option value="all">同时包含全部</option>
          <option value="any">包含任意一个</option>
        </select>
      </label>

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
      <strong>后端检索已完成，没有找到匹配的文本块</strong>
      <p>
        {{
          resultPage?.terms.length
            ? '当前按同一文本块组合关键词。可以切换“全部/任意”、减少关键词，或扩大文档范围。'
            : '当前按连续完整短语匹配。可以缩短短语、改用多个关键词，或扩大文档范围。'
        }}
      </p>
    </div>

    <div v-else-if="state === 'success' && resultPage" class="search-results">
      <header class="results-header">
        <div>
          <p>检索结果</p>
          <h2>{{ resultTitle }}</h2>
          <small>{{ resultModeDescription }}</small>
        </div>
        <span>共 {{ resultPage.pagination.total }} 个文本块</span>
      </header>

      <ol>
        <li v-for="hit in resultPage.results" :key="hit.chunkId">
          <KeywordSearchResultCard :hit="hit" :keywords="resultKeywords" />
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
  grid-template-columns: minmax(0, 1fr) minmax(170px, 200px) auto;
  align-items: end;
  gap: 14px;
  padding: 20px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface-subtle);
}

.search-mode-selector {
  display: flex;
  grid-column: 1 / -1;
  align-items: center;
  gap: 8px;
  margin: 0;
  padding: 0;
  border: 0;
}

.search-mode-selector legend {
  float: left;
  margin-right: 8px;
  color: var(--color-text-muted);
  font-size: 12px;
  font-weight: 650;
}

.search-mode-selector label {
  display: inline-flex;
  min-height: 32px;
  align-items: center;
  gap: 6px;
  padding: 0 10px;
  border: 1px solid var(--color-border);
  border-radius: 999px;
  background: var(--color-surface);
  cursor: pointer;
  font-size: 12px;
}

.search-mode-selector input {
  width: auto;
  height: auto;
  margin: 0;
  accent-color: var(--color-accent);
}

.query-field {
  display: grid;
  gap: 8px;
}

.operator-field {
  display: grid;
  gap: 8px;
  color: var(--color-text-strong);
  font-size: 13px;
  font-weight: 650;
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

.query-field > input,
.term-entry input,
.operator-field select {
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

.query-field > input:focus,
.term-entry input:focus,
.operator-field select:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

.query-field input::placeholder {
  color: var(--color-text-subtle);
}

.term-entry {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 8px;
}

.term-chips {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.term-chips > span {
  display: inline-flex;
  min-height: 28px;
  align-items: center;
  gap: 5px;
  padding: 0 8px 0 10px;
  border-radius: 999px;
  background: var(--color-accent-soft);
  color: var(--color-accent);
  font-size: 12px;
  font-weight: 650;
}

.term-chips button {
  padding: 0;
  border: 0;
  background: transparent;
  color: inherit;
  cursor: pointer;
  font-size: 16px;
  line-height: 1;
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

.results-header small {
  display: block;
  margin-top: 5px;
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

  .search-mode-selector {
    flex-wrap: wrap;
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
