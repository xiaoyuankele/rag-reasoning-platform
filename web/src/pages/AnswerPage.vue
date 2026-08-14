<script setup lang="ts">
import PageHeader from '../shared/ui/PageHeader.vue'
import PlannedFeatureCard from '../shared/ui/PlannedFeatureCard.vue'
</script>

<template>
  <div>
    <PageHeader
      eyebrow="Grounded Answer"
      title="带来源问答"
      description="围绕一次研究问题生成答案，并把证据来源放在同等重要的位置。"
    />
    <PlannedFeatureCard
      phase="计划阶段 · F4"
      title="远程模型能力不会被默认触发"
      description="正式接入时会明确展示加载、超时、重试和功能关闭状态，并避免隐藏可能产生费用的调用。"
    />
  </div>
</template>
