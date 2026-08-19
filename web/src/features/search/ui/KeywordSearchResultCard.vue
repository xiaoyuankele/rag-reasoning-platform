<script setup lang="ts">
import { computed } from 'vue'
import type { KeywordSearchHit } from '../../../entities/search-result/model/search-result'
import { highlightKeyword } from '../model/highlight-keyword'

const props = defineProps<{
  hit: KeywordSearchHit
  query: string
}>()

const documentLabel = computed(() => props.hit.title?.trim() || props.hit.originalName)
const contentSegments = computed(() => highlightKeyword(props.hit.content, props.query))

const pageLabel = computed(() => {
  const { pageStart, pageEnd } = props.hit

  if (pageStart === null || pageEnd === null) return '无固定页码'
  if (pageStart === pageEnd) return `第 ${pageStart} 页`
  return `第 ${pageStart}–${pageEnd} 页`
})
</script>

<template>
  <article class="result-card">
    <header>
      <div>
        <h3>{{ documentLabel }}</h3>
        <p v-if="hit.title?.trim()">{{ hit.originalName }}</p>
      </div>
      <span class="mime-badge">{{ hit.mimeType }}</span>
    </header>

    <p class="result-content">
      <template v-for="(segment, index) in contentSegments" :key="index">
        <mark v-if="segment.highlighted">{{ segment.text }}</mark>
        <template v-else>{{ segment.text }}</template>
      </template>
    </p>

    <footer>
      <span>{{ pageLabel }}</span>
      <span>文档 ID {{ hit.documentId }}</span>
      <span>文本块 {{ hit.chunkIndex + 1 }}</span>
    </footer>
  </article>
</template>

<style scoped>
.result-card {
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
}

header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 18px;
}

h3 {
  margin-bottom: 5px;
  font-size: 16px;
  letter-spacing: -0.015em;
}

header p {
  margin-bottom: 0;
  color: var(--color-text-subtle);
  font-size: 12px;
}

.mime-badge {
  flex: 0 0 auto;
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
}

.result-content {
  margin: 20px 0;
  color: var(--color-text-strong);
  font-size: 14px;
  line-height: 1.8;
  white-space: pre-wrap;
}

mark {
  padding: 1px 2px;
  border-radius: 3px;
  background: var(--color-highlight);
  color: inherit;
  font-weight: 650;
  box-decoration-break: clone;
}

footer {
  display: flex;
  flex-wrap: wrap;
  gap: 8px 16px;
  padding-top: 15px;
  border-top: 1px solid var(--color-border);
  color: var(--color-text-subtle);
  font-size: 12px;
}
</style>
