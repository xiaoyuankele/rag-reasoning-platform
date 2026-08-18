import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '../../features/auth/store/auth-store'

const publicAuthLayout = () => import('../layouts/PublicAuthLayout.vue')

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      component: () => import('../layouts/ProtectedAppLayout.vue'),
      meta: { requiresAuth: true },
      children: [
        {
          path: '',
          name: 'workspace',
          component: () => import('../../pages/WorkspacePage.vue'),
          meta: { title: '工作台' },
        },
        {
          path: 'documents',
          name: 'documents',
          component: () => import('../../pages/DocumentsPage.vue'),
          meta: { title: '文档库' },
        },
        {
          path: 'search',
          name: 'search',
          component: () => import('../../pages/SearchPage.vue'),
          meta: { title: '检索' },
        },
        {
          path: 'answer',
          name: 'answer',
          component: () => import('../../pages/AnswerPage.vue'),
          meta: { title: '问答' },
        },
      ],
    },
    {
      path: '/login',
      component: publicAuthLayout,
      meta: { title: '登录', guestOnly: true },
      children: [
        { path: '', name: 'login', component: () => import('../../pages/auth/LoginPage.vue') },
      ],
    },
    {
      path: '/register',
      component: publicAuthLayout,
      meta: { title: '注册', guestOnly: true },
      children: [
        {
          path: '',
          name: 'register',
          component: () => import('../../pages/auth/RegisterPage.vue'),
        },
      ],
    },
    {
      path: '/forgot-password',
      component: publicAuthLayout,
      meta: { title: '重置密码', guestOnly: true },
      children: [
        {
          path: '',
          name: 'forgot-password',
          component: () => import('../../pages/auth/ForgotPasswordPage.vue'),
        },
      ],
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
})

router.beforeEach(async (to) => {
  const authStore = useAuthStore()
  if (authStore.status === 'unknown') await authStore.restoreSession()

  if (to.meta.requiresAuth && !authStore.isAuthenticated) {
    return {
      name: 'login',
      query: to.fullPath === '/' ? undefined : { redirect: to.fullPath },
    }
  }

  if (to.meta.guestOnly && authStore.isAuthenticated) {
    return { name: 'workspace' }
  }

  return true
})

router.afterEach((to) => {
  const pageTitle = typeof to.meta.title === 'string' ? to.meta.title : '工作台'
  document.title = `${pageTitle} · RAG 研究工作台`
})
