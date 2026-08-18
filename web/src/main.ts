import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './app/App.vue'
import { router } from './app/router'
import { useAuthStore } from './features/auth/store/auth-store'
import { setAuthenticationRequiredHandler } from './shared/api/authentication-events'
import './shared/styles/index.css'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)

const authStore = useAuthStore(pinia)
setAuthenticationRequiredHandler(() => {
  authStore.markAnonymous()
  const currentRoute = router.currentRoute.value
  if (currentRoute.meta.requiresAuth) {
    void router.replace({
      name: 'login',
      query: currentRoute.fullPath === '/' ? undefined : { redirect: currentRoute.fullPath },
    })
  }
})

// 身份恢复完成后再安装 Router，避免刷新时短暂显示受保护页面。
await authStore.restoreSession()
app.use(router)
await router.isReady()
app.mount('#app')
