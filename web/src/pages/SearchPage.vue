<script setup lang="ts">
import KeywordSearchPanel from '../features/search/ui/KeywordSearchPanel.vue'
import PageHeader from '../shared/ui/PageHeader.vue'
</script>

<template>
  <div class="search-page">
    <PageHeader
      eyebrow="Keyword Retrieval"
      title="基础检索"
      description="在已经完成解析的资料中按关键词查找文本块，并保留文档、页码和原始内容。"
    />
    <KeywordSearchPanel />
  </div>
</template>

<style scoped>
.search-page {
  max-width: 1080px;
  margin: 0 auto;
}
</style>
