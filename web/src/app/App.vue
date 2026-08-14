<script setup lang="ts">
const navigationItems = [
  { label: '工作台', to: '/' },
  { label: '文档库', to: '/documents' },
  { label: '检索', to: '/search' },
  { label: '问答', to: '/answer' },
]
</script>

<template>
  <div class="app-shell">
    <aside class="sidebar">
      <RouterLink class="brand" to="/" aria-label="返回工作台首页">
        <span class="brand-mark" aria-hidden="true">R</span>
        <span>
          <strong>RAG Workspace</strong>
          <small>个人研究工作台</small>
        </span>
      </RouterLink>

      <nav class="primary-navigation" aria-label="主要导航">
        <RouterLink
          v-for="item in navigationItems"
          :key="item.to"
          :to="item.to"
          class="navigation-link"
        >
          {{ item.label }}
        </RouterLink>
      </nav>

      <p class="sidebar-note">内容优先，来源可追溯。</p>
    </aside>

    <main class="main-content">
      <RouterView />
    </main>
  </div>
</template>

<style scoped>
.app-shell {
  display: grid;
  min-height: 100vh;
  grid-template-columns: 248px minmax(0, 1fr);
}

.sidebar {
  position: sticky;
  top: 0;
  display: flex;
  height: 100vh;
  flex-direction: column;
  padding: 24px 18px;
  border-right: 1px solid var(--color-border);
  background: var(--color-surface-subtle);
}

.brand {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 2px 8px 24px;
  color: var(--color-text-strong);
  text-decoration: none;
}

.brand-mark {
  display: grid;
  width: 34px;
  height: 34px;
  flex: 0 0 auto;
  place-items: center;
  border: 1px solid var(--color-border-strong);
  border-radius: 10px;
  background: var(--color-surface);
  font-weight: 700;
}

.brand strong,
.brand small {
  display: block;
}

.brand strong {
  font-size: 14px;
  letter-spacing: -0.01em;
}

.brand small {
  margin-top: 3px;
  color: var(--color-text-muted);
  font-size: 12px;
}

.primary-navigation {
  display: grid;
  gap: 4px;
}

.navigation-link {
  padding: 10px 12px;
  border-radius: 8px;
  color: var(--color-text-muted);
  font-size: 14px;
  font-weight: 500;
  text-decoration: none;
  transition:
    background-color 150ms ease,
    color 150ms ease;
}

.navigation-link:hover {
  background: var(--color-surface-hover);
  color: var(--color-text-strong);
}

.navigation-link.router-link-active {
  background: var(--color-surface-active);
  color: var(--color-text-strong);
}

.sidebar-note {
  margin: auto 8px 0;
  color: var(--color-text-subtle);
  font-size: 12px;
}

.main-content {
  min-width: 0;
  padding: 52px clamp(28px, 6vw, 88px);
}

@media (max-width: 760px) {
  .app-shell {
    grid-template-columns: 1fr;
  }

  .sidebar {
    position: static;
    z-index: 1;
    height: auto;
    padding: 16px 20px 12px;
    border-right: 0;
    border-bottom: 1px solid var(--color-border);
  }

  .brand {
    padding: 0 0 14px;
  }

  .primary-navigation {
    display: flex;
    gap: 4px;
    overflow-x: auto;
  }

  .navigation-link {
    white-space: nowrap;
  }

  .sidebar-note {
    display: none;
  }

  .main-content {
    padding: 32px 20px;
  }
}
</style>
