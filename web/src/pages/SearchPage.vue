<script setup lang="ts">
import PageHeader from '../shared/ui/PageHeader.vue'
import PlannedFeatureCard from '../shared/ui/PlannedFeatureCard.vue'
</script>

<template>
  <div>
    <PageHeader
      eyebrow="Retrieval"
      title="检索"
      description="在完整资料或指定文档范围中查找可追溯的文本来源。"
    />
    <PlannedFeatureCard
      phase="计划阶段 · F3"
      title="关键词与语义检索将保持清晰区分"
      description="搜索词、页码和文档范围将优先保存在 URL 中，方便刷新、返回和分享当前检索状态。"
    />
  </div>
</template>
