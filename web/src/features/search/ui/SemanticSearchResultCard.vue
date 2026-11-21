<script setup lang="ts">
import { computed } from 'vue'
import type { SemanticSearchHit } from '../../../entities/search-result/model/search-result'
import { highlightKeywords } from '../model/highlight-keyword'

const props = defineProps<{
  hit: SemanticSearchHit
  query: string
}>()

const documentLabel = computed(() => props.hit.title?.trim() || props.hit.originalName)
const contentSegments = computed(() => {
  const query = props.query.trim()
  const content = props.hit.content
  const fragments = new Set<string>()

  // 语义检索不要求完整查询出现在正文中。对中文连续查询，按“最长连续命中”
  // 选择片段，避免把每个双字片段都高亮造成视觉噪声。
  for (const match of query.matchAll(/[\u3400-\u9fff]{2,}/gu)) {
    const text = match[0]
    for (let length = Math.min(8, text.length); length >= 2; length -= 1) {
      for (let start = 0; start + length <= text.length; start += 1) {
        const fragment = text.slice(start, start + length)
        if (content.includes(fragment)) fragments.add(fragment)
      }
    }
  }

  const selected = [...fragments].sort((left, right) => [...right].length - [...left].length)
  const nonOverlapping: string[] = []
  for (const fragment of selected) {
    if (!nonOverlapping.some((chosen) => chosen.includes(fragment) || fragment.includes(chosen))) {
      nonOverlapping.push(fragment)
    }
  }
  return highlightKeywords(content, [query, ...nonOverlapping])
})
const similarityLabel = computed(
  () => `${Math.round(Math.max(0, Math.min(1, props.hit.similarity)) * 100)}%`,
)
const pageLabel = computed(() => {
  const { pageStart, pageEnd } = props.hit
  if (pageStart === null) return '无固定页码'
  if (pageEnd === null || pageStart === pageEnd) return `第 ${pageStart} 页`
  return `第 ${pageStart}–${pageEnd} 页`
})
</script>

<template>
  <article class="semantic-result-card">
    <header>
      <div>
        <h3>{{ documentLabel }}</h3>
        <p v-if="hit.title?.trim()">{{ hit.originalName }}</p>
      </div>
      <div class="result-badges">
        <span>{{ hit.mimeType }}</span>
        <strong>相似度 {{ similarityLabel }}</strong>
      </div>
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
.semantic-result-card {
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

.result-badges {
  display: flex;
  flex: 0 0 auto;
  align-items: center;
  gap: 6px;
}

.result-badges span,
.result-badges strong {
  padding: 4px 8px;
  border-radius: 6px;
  background: var(--color-surface-subtle);
  color: var(--color-text-muted);
  font-size: 11px;
  font-weight: 600;
}

.result-badges strong {
  background: var(--color-accent-soft);
  color: var(--color-accent);
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

@media (max-width: 560px) {
  header {
    flex-direction: column;
  }

  .result-badges {
    flex-wrap: wrap;
  }
}
</style>
