<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  documentIdFromScope,
  type DocumentScope,
} from '../../../entities/document/model/document-scope'
import { useSemanticSearch } from '../model/use-semantic-search'
import { clearCachedSemanticSearch } from '../model/semantic-search-cache'
import SemanticSearchResultCard from './SemanticSearchResultCard.vue'

const props = defineProps<{
  cacheOwnerUserId: number
  scope: DocumentScope
  scopeIsValid: boolean
}>()

const queryInput = ref('')
const topK = ref(5)
const formError = ref('')
const retainResultInTab = ref(false)
const {
  canRetry,
  capacityFailure,
  errorMessage,
  isCoolingDown,
  isLoading,
  needsVectorization,
  requestId,
  reset,
  result,
  retainCurrentResult,
  retry,
  retryAfterSeconds,
  retryAvailable,
  restoredParams,
  search,
  state,
} = useSemanticSearch({
  cacheOwnerUserId: props.cacheOwnerUserId,
  initialDocumentId: documentIdFromScope(props.scope),
  restoreCachedResult: props.scopeIsValid,
  shouldRetainResult: () => retainResultInTab.value,
})

if (restoredParams) {
  retainResultInTab.value = true
  queryInput.value = restoredParams.query
  topK.value = restoredParams.topK
}

const queryCharacterCount = computed(() => [...queryInput.value].length)
const canSubmit = computed(
  () =>
    !isLoading.value &&
    !isCoolingDown.value &&
    props.scopeIsValid &&
    queryInput.value.trim().length > 0 &&
    queryCharacterCount.value <= 1_000,
)
const scopeKey = computed(() =>
  props.scope.kind === 'single' ? `document:${props.scope.documentId}` : 'all',
)

watch(scopeKey, () => {
  formError.value = ''
  reset({ preserveCapacity: true })
})

watch(retainResultInTab, (shouldRetain) => {
  if (shouldRetain) {
    retainCurrentResult()
  } else {
    clearCachedSemanticSearch(props.cacheOwnerUserId)
  }
})

function validateQuery(): string | null {
  const query = queryInput.value.trim()
  if (!props.scopeIsValid) {
    formError.value = '请先选择有效的文档范围。'
    return null
  }
  if (!query) {
    formError.value = '请输入需要按含义检索的问题或描述。'
    return null
  }
  if ([...query].length > 1_000) {
    formError.value = '语义检索内容不能超过 1000 个字符。'
    return null
  }

  formError.value = ''
  return query
}

function submitSearch(): void {
  const query = validateQuery()
  if (!query) return

  void search({
    query,
    documentId: documentIdFromScope(props.scope),
    topK: topK.value,
  })
}
</script>

<template>
  <section class="semantic-search-panel" aria-labelledby="semantic-search-title">
    <form class="semantic-search-form" @submit.prevent="submitSearch">
      <header>
        <div>
          <p>Meaning-based retrieval</p>
          <h2 id="semantic-search-title">按含义查找相关文本</h2>
        </div>
        <span>{{ queryCharacterCount }} / 1000</span>
      </header>

      <label class="query-field" for="semantic-query">
        <span>问题或描述</span>
        <textarea
          id="semantic-query"
          v-model="queryInput"
          rows="4"
          maxlength="1000"
          placeholder="例如：哪些因素会影响磁悬浮车辆与轨道梁的耦合振动？"
          @input="formError = ''"
        />
      </label>

      <div class="form-footer">
        <label class="top-k-field">
          <span>返回结果</span>
          <select v-model.number="topK">
            <option :value="3">3 条</option>
            <option :value="5">5 条</option>
            <option :value="10">10 条</option>
            <option :value="20">20 条</option>
          </select>
        </label>

        <button class="primary-button" type="submit" :disabled="!canSubmit">
          {{
            isLoading
              ? '正在生成查询向量并检索…'
              : isCoolingDown
                ? `服务繁忙，等待 ${retryAfterSeconds} 秒`
                : '开始语义检索'
          }}
        </button>
      </div>

      <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
      <div class="cost-notice">
        <strong>本操作会调用远程 Embedding 模型</strong>
        <span>只有点击检索按钮才会发送请求；输入、切换范围和打开页面都不会产生模型调用。</span>
      </div>

      <label class="retention-option">
        <input v-model="retainResultInTab" type="checkbox" />
        <span>
          <strong>在当前标签页保留最近结果</strong>
          <small>
            默认关闭；开启后，本次结果（含返回的文本片段）可刷新恢复，30
            分钟后或退出登录时自动清除。
          </small>
        </span>
      </label>
    </form>

    <div v-if="state === 'idle'" class="state-card state-card--quiet">
      <strong>语义检索适合概念、问题和近义表达</strong>
      <p>结果按向量相似度返回，不要求文本包含完全相同的关键词。</p>
    </div>

    <div v-else-if="state === 'loading'" class="state-card" role="status">
      <span class="loading-dot" aria-hidden="true" />
      <div>
        <strong>正在进行语义检索</strong>
        <p>后端正在生成查询向量，并在当前范围内比较已准备好的文档向量。</p>
      </div>
    </div>

    <div
      v-else-if="state === 'error'"
      class="state-card state-card--error"
      :class="{ 'state-card--capacity': capacityFailure }"
      role="alert"
      aria-live="polite"
    >
      <div>
        <strong>{{ capacityFailure?.title ?? '语义检索没有完成' }}</strong>
        <p>{{ errorMessage }}</p>
        <p v-if="capacityFailure && isCoolingDown" class="cooldown-message">
          请等待 {{ retryAfterSeconds }} 秒；倒计时结束后可手动重试。
        </p>
        <small v-if="requestId">请求编号：{{ requestId }}</small>
      </div>
      <div class="error-actions">
        <button v-if="retryAvailable" type="button" :disabled="!canRetry" @click="retry">
          {{ isCoolingDown ? `${retryAfterSeconds} 秒后可重试` : '重试本次检索' }}
        </button>
        <RouterLink v-if="needsVectorization" to="/embeddings">查看向量化状态</RouterLink>
      </div>
    </div>

    <div v-else-if="state === 'empty'" class="state-card state-card--quiet">
      <strong>语义检索已完成，没有找到相关文本块</strong>
      <p>可以换一种描述、增加返回数量，或者扩大文档范围后再次检索。</p>
    </div>

    <div v-else-if="state === 'success' && result" class="semantic-results">
      <header class="results-header">
        <div>
          <p>Semantic matches</p>
          <h2>“{{ result.query }}”</h2>
          <small>相似度用于结果排序，不代表事实正确率或回答置信度。</small>
        </div>
        <span>返回 {{ result.hits.length }} 个文本块</span>
      </header>

      <ol>
        <li v-for="hit in result.hits" :key="hit.chunkId">
          <SemanticSearchResultCard :hit="hit" :query="result.query" />
        </li>
      </ol>
    </div>
  </section>
</template>

<style scoped>
.semantic-search-panel {
  max-width: 920px;
}

.semantic-search-form,
.state-card {
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

.semantic-search-form {
  display: grid;
  gap: 16px;
  padding: 20px;
  background: var(--color-surface-subtle);
}

.semantic-search-form > header,
.results-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.semantic-search-form header p,
.results-header p {
  margin-bottom: 5px;
  color: var(--color-accent);
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.semantic-search-form h2,
.results-header h2 {
  margin: 0;
  font-size: 17px;
}

.semantic-search-form header > span,
.results-header > span,
.results-header small {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.query-field,
.top-k-field {
  display: grid;
  gap: 7px;
  color: var(--color-text-strong);
  font-size: 12px;
  font-weight: 650;
}

.query-field textarea,
.top-k-field select {
  width: 100%;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  outline: none;
  background: var(--color-surface);
  color: var(--color-text-strong);
  font: inherit;
}

.query-field textarea {
  min-height: 112px;
  padding: 12px;
  resize: vertical;
  line-height: 1.65;
}

.top-k-field select {
  min-width: 130px;
  height: 42px;
  padding: 0 30px 0 10px;
}

.query-field textarea:focus,
.top-k-field select:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

.form-footer {
  display: flex;
  align-items: end;
  justify-content: space-between;
  gap: 12px;
}

.primary-button {
  min-height: 42px;
  padding: 0 16px;
  border: 0;
  border-radius: 9px;
  background: var(--color-accent);
  color: white;
  cursor: pointer;
  font-weight: 700;
}

.primary-button:disabled,
.error-actions button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.form-error {
  margin: 0;
  color: var(--color-danger);
  font-size: 12px;
}

.cost-notice {
  display: grid;
  gap: 4px;
  padding: 11px 12px;
  border-radius: 9px;
  background: #f7f1e7;
  color: var(--color-text-muted);
  font-size: 11px;
  line-height: 1.55;
}

.cost-notice strong {
  color: var(--color-text-strong);
}

.retention-option {
  display: flex;
  align-items: flex-start;
  gap: 9px;
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: 12px;
  font-weight: 400;
}

.retention-option input {
  width: auto;
  height: auto;
  margin-top: 3px;
  accent-color: var(--color-accent);
}

.retention-option span {
  display: grid;
  gap: 2px;
}

.retention-option strong {
  color: var(--color-text-strong);
  font-size: 12px;
}

.retention-option small {
  color: var(--color-text-subtle);
  font-size: 11px;
  line-height: 1.5;
}

.state-card {
  display: flex;
  min-height: 112px;
  align-items: center;
  justify-content: space-between;
  gap: 18px;
  margin-top: 22px;
  padding: 22px;
}

.state-card--quiet {
  display: block;
  border-style: dashed;
  background: var(--color-surface-subtle);
}

.state-card--error {
  align-items: flex-start;
  border-color: #ead0cc;
  background: var(--color-danger-soft);
}

.state-card--capacity {
  border-color: #dccb9a;
  background: #fff9e8;
}

.state-card p {
  margin: 6px 0 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.6;
}

.state-card small {
  display: block;
  margin-top: 7px;
  color: var(--color-text-subtle);
}

.cooldown-message {
  color: #7a5a12 !important;
}

.loading-dot {
  width: 10px;
  height: 10px;
  flex: 0 0 auto;
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 5px var(--color-accent-soft);
}

.state-card:has(.loading-dot) {
  justify-content: flex-start;
}

.error-actions {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  flex-wrap: wrap;
  gap: 12px;
}

.error-actions button {
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-accent);
  cursor: pointer;
  font-weight: 650;
}

.error-actions a {
  color: var(--color-accent);
  font-size: 12px;
  font-weight: 650;
  text-decoration: none;
}

.semantic-results {
  margin-top: 30px;
}

.results-header {
  align-items: flex-end;
  margin-bottom: 16px;
}

.results-header h2 {
  font-size: 21px;
  letter-spacing: -0.025em;
}

.results-header small {
  display: block;
  margin-top: 5px;
}

.semantic-results ol {
  display: grid;
  gap: 14px;
  margin: 0;
  padding: 0;
  list-style: none;
}

@media (max-width: 560px) {
  .semantic-search-form > header,
  .results-header,
  .form-footer,
  .state-card--error {
    align-items: flex-start;
    flex-direction: column;
  }
}
</style>
