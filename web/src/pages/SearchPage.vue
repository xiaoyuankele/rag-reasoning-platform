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
import PageHeader from '../shared/ui/PageHeader.vue'

const route = useRoute()
const router = useRouter()

const parsedScope = computed(() => parseDocumentScopeQuery(route.query.document_id))

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
      eyebrow="Keyword Retrieval"
      title="基础检索"
      description="在已经完成解析的资料中按关键词查找文本块，并保留文档、页码和原始内容。"
    />
    <DocumentScopePicker
      :model-value="parsedScope.scope"
      :invalid-selection="!parsedScope.isValid"
      @update:model-value="updateScope"
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
