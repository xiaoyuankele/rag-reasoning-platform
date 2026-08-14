<script setup lang="ts">
defineProps<{
  phase: string
  title: string
  description: string
}>()
</script>

<template>
  <section class="planned-card">
    <span>{{ phase }}</span>
    <h2>{{ title }}</h2>
    <p>{{ description }}</p>
  </section>
</template>

<style scoped>
.planned-card {
  max-width: 720px;
  padding: 26px;
  border: 1px dashed var(--color-border-strong);
  border-radius: 14px;
  background: var(--color-surface-subtle);
}

span {
  color: var(--color-text-subtle);
  font-size: 12px;
  font-weight: 650;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

h2 {
  margin: 12px 0 10px;
  font-size: 20px;
  letter-spacing: -0.025em;
}

p {
  margin-bottom: 0;
  color: var(--color-text-muted);
  line-height: 1.7;
}
</style>
