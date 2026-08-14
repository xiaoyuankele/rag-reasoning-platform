<script setup lang="ts">
import PageHeader from '../shared/ui/PageHeader.vue'
import PlannedFeatureCard from '../shared/ui/PlannedFeatureCard.vue'
</script>

<template>
  <div>
    <PageHeader
      eyebrow="Documents"
      title="文档库"
      description="管理上传的资料、处理状态和可检索内容。"
    />
    <PlannedFeatureCard
      phase="计划阶段 · F2"
      title="文档管理将在 API 基础稳定后接入"
      description="后续将覆盖上传、列表、详情、解析任务轮询和删除确认；当前页面只验证路由边界。"
    />
  </div>
</template>
