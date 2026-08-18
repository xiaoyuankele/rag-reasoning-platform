<script setup lang="ts">
import { ref } from 'vue'
import DocumentBatchImportPanel from '../features/documents/ui/DocumentBatchImportPanel.vue'
import DocumentDetailPanel from '../features/documents/ui/DocumentDetailPanel.vue'
import DocumentLibraryPanel from '../features/documents/ui/DocumentLibraryPanel.vue'
import PageHeader from '../shared/ui/PageHeader.vue'

const selectedDocumentId = ref<number | null>(null)
const libraryRefreshToken = ref(0)

function refreshLibrary(): void {
  libraryRefreshToken.value += 1
}

function handleDeleted(): void {
  selectedDocumentId.value = null
  refreshLibrary()
}
</script>

<template>
  <div>
    <PageHeader
      eyebrow="Documents"
      title="文档库"
      description="批量导入、解析并查看当前账户的资料；重复内容会安全复用已有记录。"
    />
    <DocumentBatchImportPanel @changed="refreshLibrary" @select="selectedDocumentId = $event" />
    <div class="documents-workspace" :class="{ 'documents-workspace--detail': selectedDocumentId }">
      <DocumentLibraryPanel
        :selected-document-id="selectedDocumentId"
        :refresh-token="libraryRefreshToken"
        @select="selectedDocumentId = $event"
      />
      <DocumentDetailPanel
        :document-id="selectedDocumentId"
        @close="selectedDocumentId = null"
        @deleted="handleDeleted"
        @updated="refreshLibrary"
      />
    </div>
  </div>
</template>

<style scoped>
.documents-workspace {
  display: grid;
  min-width: 0;
  gap: 24px;
}

.documents-workspace--detail {
  grid-template-columns: minmax(520px, 1fr) minmax(340px, 420px);
  align-items: start;
}

.documents-workspace--detail :deep(.detail-panel) {
  position: sticky;
  top: 24px;
  max-height: calc(100vh - 48px);
  overflow-y: auto;
}

@media (max-width: 1120px) {
  .documents-workspace--detail {
    grid-template-columns: 1fr;
  }

  .documents-workspace--detail :deep(.detail-panel) {
    position: static;
    max-height: none;
    grid-row: 1;
  }
}
</style>
