<script setup lang="ts">
import { computed, ref } from 'vue'
import { useRouter } from 'vue-router'
import { presentAuthError } from '../../features/auth/model/auth-error-message'
import { useAuthStore } from '../../features/auth/store/auth-store'

const navigationItems = [
  { label: '工作台', to: '/' },
  { label: '文档库', to: '/documents' },
  { label: '向量化', to: '/embeddings' },
  { label: '检索', to: '/search' },
  { label: '问答', to: '/answer' },
]

const authStore = useAuthStore()
const router = useRouter()
const isLoggingOut = ref(false)
const logoutError = ref<string | null>(null)
const userContact = computed(() => authStore.user?.email ?? authStore.user?.phone ?? '')

async function handleLogout(): Promise<void> {
  isLoggingOut.value = true
  logoutError.value = null
  try {
    await authStore.logout()
    await router.replace({ name: 'login' })
  } catch (error) {
    logoutError.value = presentAuthError(error, '退出失败，请稍后重试。').message
  } finally {
    isLoggingOut.value = false
  }
}
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

      <div class="account-panel">
        <p v-if="logoutError" class="logout-error" role="alert">{{ logoutError }}</p>
        <strong>{{ authStore.user?.displayName }}</strong>
        <span>{{ userContact }}</span>
        <button type="button" :disabled="isLoggingOut" @click="handleLogout">
          {{ isLoggingOut ? '正在退出…' : '退出登录' }}
        </button>
      </div>
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

.navigation-link.router-link-exact-active {
  background: var(--color-surface-active);
  color: var(--color-text-strong);
}

.account-panel {
  display: grid;
  gap: 4px;
  margin-top: auto;
  padding: 14px 10px 4px;
  border-top: 1px solid var(--color-border);
}

.account-panel strong {
  overflow: hidden;
  font-size: 13px;
  text-overflow: ellipsis;
}

.account-panel span {
  overflow: hidden;
  color: var(--color-text-subtle);
  font-size: 11px;
  text-overflow: ellipsis;
}

.account-panel button {
  width: fit-content;
  margin-top: 8px;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--color-text-muted);
  cursor: pointer;
  font-size: 12px;
}

.account-panel button:hover {
  color: var(--color-text-strong);
}

.account-panel button:disabled {
  cursor: wait;
  opacity: 0.6;
}

.logout-error {
  margin-bottom: 6px;
  color: var(--color-danger);
  font-size: 11px;
  line-height: 1.4;
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

  .account-panel {
    display: flex;
    align-items: center;
    gap: 10px;
    margin-top: 12px;
    padding: 10px 0 0;
  }

  .account-panel button {
    margin: 0 0 0 auto;
  }

  .account-panel span,
  .logout-error {
    display: none;
  }

  .main-content {
    padding: 32px 20px;
  }
}
</style>
