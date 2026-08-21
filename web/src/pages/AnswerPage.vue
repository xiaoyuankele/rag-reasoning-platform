<script setup lang="ts">
import { computed } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  documentIdFromScope,
  parseDocumentScopeQuery,
  type DocumentScope,
} from '../entities/document/model/document-scope'
import GroundedAnswerPanel from '../features/answer/ui/GroundedAnswerPanel.vue'
import DocumentScopePicker from '../features/documents/ui/DocumentScopePicker.vue'
import PageHeader from '../shared/ui/PageHeader.vue'

const route = useRoute()
const router = useRouter()
const parsedScope = computed(() => parseDocumentScopeQuery(route.query.document_id))

function updateScope(scope: DocumentScope): void {
  void router.push({
    name: 'answer',
    query: { document_id: documentIdFromScope(scope)?.toString() },
  })
}
</script>

<template>
  <div class="answer-page">
    <PageHeader
      eyebrow="Grounded Answer"
      title="带来源问答"
      description="从当前账户的向量证据中回答研究问题，并同步展示来源、页码、相似度和 Token 用量。"
    />
    <DocumentScopePicker
      :model-value="parsedScope.scope"
      :invalid-selection="!parsedScope.isValid"
      @update:model-value="updateScope"
    />
    <GroundedAnswerPanel :scope="parsedScope.scope" :scope-is-valid="parsedScope.isValid" />
  </div>
</template>

<style scoped>
.answer-page {
  max-width: 1120px;
  margin: 0 auto;
}
</style>
