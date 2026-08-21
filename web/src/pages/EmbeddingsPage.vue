<script setup lang="ts">
import EmbeddingWorkspacePanel from '../features/embeddings/ui/EmbeddingWorkspacePanel.vue'
import { listDocuments } from '../features/documents/api/document-api'
import PageHeader from '../shared/ui/PageHeader.vue'
</script>

<template>
  <div class="embeddings-page">
    <PageHeader
      eyebrow="Vectorization"
      title="文档向量化"
      description="为单篇或多篇资料创建向量任务，集中查看等待、执行、完成与失败状态。"
    />
    <EmbeddingWorkspacePanel :load-document-page="listDocuments" />
  </div>
</template>

<style scoped>
.embeddings-page {
  max-width: 1180px;
  margin: 0 auto;
}
</style>
