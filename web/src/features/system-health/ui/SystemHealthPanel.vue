<script setup lang="ts">
import { computed } from 'vue'
import { useSystemHealth } from '../model/use-system-health'

const { errorMessage, isLoading, refresh, snapshot, state } = useSystemHealth()

const statusTitle = computed(() => {
  if (state.value === 'loading') return '正在连接后端'
  if (state.value === 'success') return '后端运行正常'
  if (state.value === 'error') return '后端暂时不可连接'
  return '尚未检查后端状态'
})

const checkedAtLabel = computed(() => {
  if (!snapshot.value) return ''

  return new Intl.DateTimeFormat('zh-CN', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(snapshot.value.checkedAt)
})
</script>

<template>
  <section class="health-panel" aria-labelledby="health-title">
    <div>
      <p class="section-label">系统连接</p>
      <h2 id="health-title">开发环境状态</h2>
    </div>

    <div class="health-status" aria-live="polite">
      <span
        class="status-dot"
        :class="{
          'status-dot--online': state === 'success',
          'status-dot--error': state === 'error',
        }"
        aria-hidden="true"
      />
      <div class="status-copy">
        <strong>{{ statusTitle }}</strong>
        <p v-if="state === 'success'">最近检查：{{ checkedAtLabel }}</p>
        <p v-else-if="state === 'error'" role="alert">{{ errorMessage }}</p>
        <p v-else>通过统一 API Client 请求 GET /health</p>
      </div>
      <button class="secondary-button" type="button" :disabled="isLoading" @click="refresh">
        {{ isLoading ? '检查中' : '重新检查' }}
      </button>
    </div>
  </section>
</template>

<style scoped>
.health-panel {
  display: grid;
  gap: 24px;
  padding: 24px;
  border: 1px solid var(--color-border);
  border-radius: 14px;
  background: var(--color-surface);
  box-shadow: var(--shadow-soft);
}

.section-label {
  margin-bottom: 7px;
  color: var(--color-text-subtle);
  font-size: 12px;
  font-weight: 650;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

h2 {
  margin-bottom: 0;
  font-size: 18px;
  letter-spacing: -0.02em;
}

.health-status {
  display: grid;
  grid-template-columns: auto minmax(0, 1fr) auto;
  align-items: center;
  gap: 12px;
  padding-top: 20px;
  border-top: 1px solid var(--color-border);
}

.status-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  background: var(--color-text-subtle);
  box-shadow: 0 0 0 4px var(--color-surface-hover);
}

.status-dot--online {
  background: var(--color-accent);
  box-shadow: 0 0 0 4px var(--color-accent-soft);
}

.status-dot--error {
  background: var(--color-danger);
  box-shadow: 0 0 0 4px var(--color-danger-soft);
}

.status-copy strong {
  display: block;
  font-size: 14px;
}

.status-copy p {
  margin: 5px 0 0;
  color: var(--color-text-muted);
  font-size: 13px;
  line-height: 1.5;
}

.secondary-button {
  padding: 8px 12px;
  border: 1px solid var(--color-border-strong);
  border-radius: 8px;
  background: var(--color-surface);
  color: var(--color-text-strong);
  cursor: pointer;
  font-size: 13px;
  font-weight: 600;
}

.secondary-button:hover:not(:disabled) {
  background: var(--color-surface-hover);
}

.secondary-button:disabled {
  cursor: wait;
  opacity: 0.6;
}

@media (max-width: 560px) {
  .health-status {
    grid-template-columns: auto minmax(0, 1fr);
  }

  .secondary-button {
    grid-column: 1 / -1;
    justify-self: start;
    margin-top: 4px;
  }
}
</style>
