<script setup lang="ts">
defineProps<{
  eyebrow: string
  title: string
  description: string
}>()
</script>

<template>
  <header class="page-header">
    <p>{{ eyebrow }}</p>
    <h1>{{ title }}</h1>
    <span>{{ description }}</span>
  </header>
</template>

<style scoped>
.page-header {
  max-width: 760px;
  margin-bottom: 36px;
}

p {
  margin-bottom: 10px;
  color: var(--color-accent);
  font-size: 12px;
  font-weight: 700;
  letter-spacing: 0.08em;
  text-transform: uppercase;
}

h1 {
  margin-bottom: 14px;
  font-size: clamp(30px, 4vw, 46px);
  font-weight: 650;
  letter-spacing: -0.045em;
  line-height: 1.08;
}

span {
  display: block;
  max-width: 680px;
  color: var(--color-text-muted);
  font-size: 16px;
  line-height: 1.75;
}
</style>
