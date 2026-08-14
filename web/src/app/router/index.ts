import { createRouter, createWebHistory } from 'vue-router'

export const router = createRouter({
  history: createWebHistory(import.meta.env.BASE_URL),
  routes: [
    {
      path: '/',
      name: 'workspace',
      component: () => import('../../pages/WorkspacePage.vue'),
      meta: { title: '工作台' },
    },
    {
      path: '/documents',
      name: 'documents',
      component: () => import('../../pages/DocumentsPage.vue'),
      meta: { title: '文档库' },
    },
    {
      path: '/search',
      name: 'search',
      component: () => import('../../pages/SearchPage.vue'),
      meta: { title: '检索' },
    },
    {
      path: '/answer',
      name: 'answer',
      component: () => import('../../pages/AnswerPage.vue'),
      meta: { title: '问答' },
    },
    {
      path: '/:pathMatch(.*)*',
      redirect: '/',
    },
  ],
})

router.afterEach((to) => {
  const pageTitle = typeof to.meta.title === 'string' ? to.meta.title : '工作台'
  document.title = `${pageTitle} · RAG 研究工作台`
})
