<script setup lang="ts">
import SystemHealthPanel from '../features/system-health/ui/SystemHealthPanel.vue'
import PageHeader from '../shared/ui/PageHeader.vue'

const architectureLayers = [
  { name: '页面与路由', detail: '组合功能，保持 URL 可恢复' },
  { name: '功能与状态', detail: '组织用户动作与异步状态' },
  { name: 'API 与契约', detail: '统一请求、错误和 DTO 边界' },
]
</script>

<template>
  <div class="workspace-page">
    <PageHeader
      eyebrow="Foundation · F0"
      title="从清晰的架构开始"
      description="当前前端已经具备路由、状态入口、统一请求层和自动化测试基础。后续能力会按文档管理、检索、问答的顺序接入。"
    />

    <section class="architecture-grid" aria-label="前端架构分层">
      <article v-for="(layer, index) in architectureLayers" :key="layer.name">
        <span>0{{ index + 1 }}</span>
        <h2>{{ layer.name }}</h2>
        <p>{{ layer.detail }}</p>
      </article>
    </section>

    <SystemHealthPanel />
  </div>
</template>

<style scoped>
.workspace-page {
  max-width: 1080px;
  margin: 0 auto;
}

.architecture-grid {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  margin-bottom: 28px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  overflow: hidden;
}

article {
  min-height: 150px;
  padding: 22px;
  background: var(--color-surface);
}

article + article {
  border-left: 1px solid var(--color-border);
}

article span {
  color: var(--color-text-subtle);
  font-size: 12px;
  font-weight: 700;
}

article h2 {
  margin: 26px 0 8px;
  font-size: 16px;
}

article p {
  margin-bottom: 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.55;
}

@media (max-width: 850px) {
  .architecture-grid {
    grid-template-columns: 1fr;
  }

  article + article {
    border-top: 1px solid var(--color-border);
    border-left: 0;
  }
}
</style>
