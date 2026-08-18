<script setup lang="ts">
import DocumentLibraryPanel from '../features/documents/ui/DocumentLibraryPanel.vue'
import PageHeader from '../shared/ui/PageHeader.vue'
</script>

<template>
  <div>
    <PageHeader
      eyebrow="Documents"
      title="文档库"
      description="上传并查看当前账户的资料；重复内容会安全返回已有记录。"
    />
    <DocumentLibraryPanel />
  </div>
</template>
