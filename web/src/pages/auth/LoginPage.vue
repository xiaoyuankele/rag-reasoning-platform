<script setup lang="ts">
import { computed, reactive, ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import {
  presentAuthError,
  type AuthErrorPresentation,
} from '../../features/auth/model/auth-error-message'
import { useAuthStore } from '../../features/auth/store/auth-store'

const authStore = useAuthStore()
const route = useRoute()
const router = useRouter()
const form = reactive({ identifier: '', password: '' })
const isSubmitting = ref(false)
const formError = ref<AuthErrorPresentation | null>(null)

const resetSucceeded = computed(() => route.query.reset === 'success')

function safeRedirectTarget(): string {
  const requested = route.query.redirect
  if (typeof requested === 'string' && requested.startsWith('/') && !requested.startsWith('//')) {
    return requested
  }
  return '/'
}

async function handleSubmit(): Promise<void> {
  formError.value = null
  if (!form.identifier.trim() || !form.password) {
    formError.value = { message: '请输入邮箱和密码。' }
    return
  }

  isSubmitting.value = true
  try {
    await authStore.login({
      identifier: form.identifier.trim(),
      password: form.password,
    })
    await router.replace(safeRedirectTarget())
  } catch (error) {
    formError.value = presentAuthError(error, '登录未能完成，请稍后重试。')
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <section class="auth-card" aria-labelledby="login-title">
    <header class="auth-card-header">
      <p>Welcome back</p>
      <h1 id="login-title">登录工作台</h1>
      <span>使用已验证的邮箱进入你的私有文档空间。</span>
    </header>

    <p v-if="resetSucceeded" class="auth-success" role="status">密码已经重置，请使用新密码登录。</p>
    <p v-if="authStore.restoreError" class="auth-alert" role="alert">
      {{ authStore.restoreError }}你仍可以重新尝试登录。
    </p>

    <form class="auth-form" novalidate @submit.prevent="handleSubmit">
      <div class="auth-field">
        <label for="login-identifier">邮箱</label>
        <input
          id="login-identifier"
          v-model="form.identifier"
          type="email"
          autocomplete="username"
          inputmode="email"
          required
        />
      </div>

      <div class="auth-field">
        <label for="login-password">密码</label>
        <input
          id="login-password"
          v-model="form.password"
          type="password"
          autocomplete="current-password"
          required
        />
      </div>

      <p v-if="formError" class="auth-alert" role="alert">
        {{ formError.message }}
        <span v-if="formError.requestId" class="auth-request-id">
          请求编号：{{ formError.requestId }}
        </span>
      </p>

      <button class="auth-primary-button" type="submit" :disabled="isSubmitting">
        {{ isSubmitting ? '正在登录…' : '登录' }}
      </button>
    </form>

    <nav class="auth-links" aria-label="其他认证操作">
      <RouterLink to="/register">创建账户</RouterLink>
      <RouterLink to="/forgot-password">忘记密码</RouterLink>
    </nav>
  </section>
</template>
