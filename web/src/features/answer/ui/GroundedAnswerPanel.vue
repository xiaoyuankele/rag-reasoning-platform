<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import type { AnswerResponseLanguage } from '../../../entities/answer/model/grounded-answer'
import {
  documentIdFromScope,
  type DocumentScope,
} from '../../../entities/document/model/document-scope'
import { useGroundedAnswer } from '../model/use-grounded-answer'

const props = defineProps<{
  scope: DocumentScope
  scopeIsValid: boolean
}>()

const queryInput = ref('')
const responseLanguage = ref<AnswerResponseLanguage>('auto')
const topK = ref(5)
const formError = ref('')
const { ask, canRetry, errorMessage, isLoading, requestId, reset, result, retry, state } =
  useGroundedAnswer()

const queryCharacterCount = computed(() => [...queryInput.value].length)
const canSubmit = computed(
  () =>
    !isLoading.value &&
    props.scopeIsValid &&
    queryInput.value.trim().length > 0 &&
    queryCharacterCount.value <= 1000,
)

const scopeKey = computed(() =>
  props.scope.kind === 'single' ? `document:${props.scope.documentId}` : 'all',
)

watch(scopeKey, () => {
  formError.value = ''
  reset()
})

function validateQuestion(): string | null {
  const query = queryInput.value.trim()
  if (!props.scopeIsValid) {
    formError.value = '请先选择有效的文档范围。'
    return null
  }
  if (!query) {
    formError.value = '请输入研究问题。'
    return null
  }
  if ([...query].length > 1000) {
    formError.value = '问题不能超过 1000 个字符。'
    return null
  }
  formError.value = ''
  return query
}

function submitQuestion(): void {
  const query = validateQuestion()
  if (!query) return

  void ask({
    query,
    documentId: documentIdFromScope(props.scope),
    topK: topK.value,
    responseLanguage: responseLanguage.value,
  })
}

function displaySourceTitle(title: string | null, originalName: string): string {
  return title?.trim() || originalName
}

function pageLabel(pageStart: number | null, pageEnd: number | null): string {
  if (pageStart === null) return '页码未知'
  if (pageEnd === null || pageStart === pageEnd) return `第 ${pageStart} 页`
  return `第 ${pageStart}–${pageEnd} 页`
}

function similarityLabel(similarity: number): string {
  return `${Math.round(Math.max(0, Math.min(1, similarity)) * 100)}%`
}
</script>

<template>
  <section class="answer-workspace" aria-labelledby="answer-form-title">
    <form class="question-card" @submit.prevent="submitQuestion">
      <header>
        <div>
          <p>Research question</p>
          <h2 id="answer-form-title">提出一个研究问题</h2>
        </div>
        <span>{{ queryCharacterCount }} / 1000</span>
      </header>

      <label class="question-field" for="answer-query">
        <span>问题</span>
        <textarea
          id="answer-query"
          v-model="queryInput"
          rows="5"
          maxlength="1000"
          placeholder="例如：磁悬浮车辆与轨道梁耦合振动的主要影响因素是什么？"
          @input="formError = ''"
        />
      </label>

      <div class="answer-options">
        <label>
          <span>回答语言</span>
          <select v-model="responseLanguage">
            <option value="auto">跟随问题</option>
            <option value="zh">中文</option>
            <option value="en">English</option>
          </select>
        </label>
        <label>
          <span>最多引用</span>
          <select v-model.number="topK">
            <option :value="3">3 条</option>
            <option :value="5">5 条</option>
            <option :value="8">8 条</option>
            <option :value="10">10 条</option>
          </select>
        </label>
      </div>

      <p v-if="formError" class="form-error" role="alert">{{ formError }}</p>
      <div class="cost-notice">
        <strong>本操作会调用远程模型</strong>
        <span>只有点击下方按钮才会发送请求；页面打开和范围切换不会产生模型调用。</span>
      </div>

      <button class="primary-button" type="submit" :disabled="!canSubmit">
        {{ isLoading ? '正在检索证据并生成回答…' : '生成带来源回答' }}
      </button>
    </form>

    <div v-if="state === 'idle'" class="answer-state answer-state--quiet">
      <strong>回答会严格基于当前范围内的向量证据</strong>
      <p>如果文档尚未向量化，页面会给出明确提示，不会退回无来源的自由回答。</p>
    </div>

    <div v-else-if="state === 'loading'" class="answer-state" role="status" aria-live="polite">
      <span class="loading-dot" aria-hidden="true"></span>
      <div>
        <strong>正在生成回答</strong>
        <p>后端正在检索语义证据并调用生成模型，通常需要数秒。</p>
      </div>
    </div>

    <div v-else-if="state === 'error'" class="answer-state answer-state--error" role="alert">
      <div>
        <strong>本次问答没有完成</strong>
        <p>{{ errorMessage }}</p>
        <small v-if="requestId">请求编号：{{ requestId }}</small>
      </div>
      <div class="error-actions">
        <button v-if="canRetry" type="button" @click="retry">重试本次问题</button>
        <RouterLink to="/embeddings">查看向量化状态</RouterLink>
      </div>
    </div>

    <article
      v-else-if="result"
      class="answer-result"
      :class="{ 'answer-result--insufficient': state === 'insufficient-evidence' }"
    >
      <header>
        <div>
          <p>{{ state === 'insufficient-evidence' ? 'Evidence check' : 'Grounded answer' }}</p>
          <h2>{{ state === 'insufficient-evidence' ? '证据不足' : '回答' }}</h2>
        </div>
        <span>{{ result.sources.length }} 条来源</span>
      </header>

      <p class="answer-text">{{ result.answer }}</p>

      <dl class="usage-summary" aria-label="本次回答用量">
        <div>
          <dt>Prompt</dt>
          <dd>{{ result.usage.promptTokens }}</dd>
        </div>
        <div>
          <dt>Completion</dt>
          <dd>{{ result.usage.completionTokens }}</dd>
        </div>
        <div>
          <dt>Total</dt>
          <dd>{{ result.usage.totalTokens }}</dd>
        </div>
      </dl>

      <section
        v-if="result.sources.length > 0"
        class="source-section"
        aria-labelledby="source-title"
      >
        <div class="source-heading">
          <div>
            <p>Evidence</p>
            <h3 id="source-title">引用来源</h3>
          </div>
          <span>编号与回答中的 [1]、[2] 对应</span>
        </div>
        <ol>
          <li v-for="source in result.sources" :key="source.chunkId">
            <span class="citation-number">[{{ source.citation }}]</span>
            <div>
              <strong>{{ displaySourceTitle(source.title, source.originalName) }}</strong>
              <p v-if="source.title?.trim() && source.title.trim() !== source.originalName">
                {{ source.originalName }}
              </p>
              <small>
                {{ pageLabel(source.pageStart, source.pageEnd) }} · 文档 #{{ source.documentId }} ·
                文本块 {{ source.chunkIndex }} · 相似度 {{ similarityLabel(source.similarity) }}
              </small>
            </div>
          </li>
        </ol>
      </section>
      <p v-else class="insufficient-note">
        后端没有检索到足够证据，因此跳过了生成模型；本次 Token 用量应为 0。
      </p>
    </article>
  </section>
</template>

<style scoped>
.answer-workspace {
  display: grid;
  grid-template-columns: minmax(0, 0.82fr) minmax(0, 1.18fr);
  align-items: start;
  gap: 18px;
}

.question-card,
.answer-state,
.answer-result {
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

.question-card {
  display: grid;
  gap: 18px;
  padding: 22px;
}

.question-card > header,
.answer-result > header,
.source-heading {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 16px;
}

.question-card header p,
.answer-result header p,
.source-heading p {
  margin-bottom: 5px;
  color: var(--color-accent);
  font-size: 10px;
  font-weight: 750;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

.question-card h2,
.answer-result h2,
.source-heading h3 {
  margin: 0;
  font-size: 17px;
}

.question-card header > span,
.answer-result header > span,
.source-heading > span {
  color: var(--color-text-subtle);
  font-size: 11px;
}

.question-field,
.answer-options label {
  display: grid;
  gap: 8px;
  color: var(--color-text-strong);
  font-size: 12px;
  font-weight: 650;
}

.question-field textarea,
.answer-options select {
  width: 100%;
  border: 1px solid var(--color-border-strong);
  border-radius: 9px;
  outline: none;
  background: var(--color-surface);
  color: var(--color-text-strong);
  font: inherit;
}

.question-field textarea {
  min-height: 138px;
  padding: 12px;
  resize: vertical;
  line-height: 1.65;
}

.answer-options select {
  height: 40px;
  padding: 0 32px 0 10px;
}

.question-field textarea:focus,
.answer-options select:focus {
  border-color: var(--color-accent);
  box-shadow: 0 0 0 3px var(--color-accent-soft);
}

.answer-options {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
}

.form-error {
  margin: -6px 0 0;
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

.primary-button:disabled {
  cursor: not-allowed;
  opacity: 0.55;
}

.answer-state {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  min-height: 150px;
  padding: 22px;
  color: var(--color-text-muted);
}

.answer-state--quiet {
  background: var(--color-surface-subtle);
}

.answer-state--error {
  flex-direction: column;
  background: var(--color-danger-soft);
}

.answer-state strong {
  color: var(--color-text-strong);
}

.answer-state p {
  margin: 6px 0 0;
  font-size: 13px;
  line-height: 1.65;
}

.answer-state small {
  display: block;
  margin-top: 7px;
}

.loading-dot {
  width: 10px;
  height: 10px;
  margin-top: 5px;
  border-radius: 50%;
  background: var(--color-accent);
  box-shadow: 0 0 0 6px var(--color-accent-soft);
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

.answer-result {
  padding: 24px;
}

.answer-result--insufficient {
  background: var(--color-surface-subtle);
}

.answer-text {
  margin: 22px 0;
  color: var(--color-text-strong);
  font-size: 15px;
  line-height: 1.85;
  white-space: pre-wrap;
}

.usage-summary {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 8px;
  margin: 0;
  padding: 12px;
  border-radius: 10px;
  background: var(--color-surface-subtle);
}

.usage-summary div {
  text-align: center;
}

.usage-summary dt {
  color: var(--color-text-subtle);
  font-size: 10px;
}

.usage-summary dd {
  margin: 3px 0 0;
  color: var(--color-text-strong);
  font-size: 13px;
  font-weight: 700;
}

.source-section {
  margin-top: 24px;
  padding-top: 20px;
  border-top: 1px solid var(--color-border);
}

.source-section ol {
  display: grid;
  gap: 10px;
  margin: 15px 0 0;
  padding: 0;
  list-style: none;
}

.source-section li {
  display: grid;
  grid-template-columns: 36px minmax(0, 1fr);
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--color-border);
  border-radius: 10px;
}

.citation-number {
  color: var(--color-accent);
  font-size: 13px;
  font-weight: 750;
}

.source-section strong {
  color: var(--color-text-strong);
  font-size: 13px;
}

.source-section li p {
  margin: 3px 0;
  color: var(--color-text-muted);
  font-size: 11px;
}

.source-section li small {
  color: var(--color-text-subtle);
  line-height: 1.5;
}

.insufficient-note {
  margin: 18px 0 0;
  color: var(--color-text-muted);
  font-size: 12px;
  line-height: 1.65;
}

@media (max-width: 860px) {
  .answer-workspace {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 560px) {
  .answer-options,
  .usage-summary {
    grid-template-columns: 1fr;
  }

  .answer-state--error,
  .source-heading {
    flex-direction: column;
  }
}
</style>
