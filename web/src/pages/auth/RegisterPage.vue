<script setup lang="ts">
import { reactive, ref } from 'vue'
import { useRouter } from 'vue-router'
import {
  presentAuthError,
  type AuthErrorPresentation,
} from '../../features/auth/model/auth-error-message'
import {
  isValidEmail,
  isValidVerificationCode,
  validatePassword,
} from '../../features/auth/model/auth-validation'
import { useVerificationChallenge } from '../../features/auth/model/use-verification-challenge'
import { useAuthStore } from '../../features/auth/store/auth-store'

const authStore = useAuthStore()
const router = useRouter()
const verification = useVerificationChallenge('register')
const form = reactive({
  email: '',
  verificationCode: '',
  displayName: '',
  password: '',
  confirmPassword: '',
})
const isSubmitting = ref(false)
const formError = ref<AuthErrorPresentation | null>(null)

async function sendCode(): Promise<void> {
  formError.value = null
  if (!isValidEmail(form.email)) {
    formError.value = { message: '请输入有效的邮箱地址。' }
    return
  }
  await verification.request(form.email)
}

function changeEmail(): void {
  verification.clear()
  form.verificationCode = ''
}

async function handleSubmit(): Promise<void> {
  formError.value = null
  const challenge = verification.challenge.value
  if (!challenge) {
    formError.value = { message: '请先获取邮箱验证码。' }
    return
  }
  if (!isValidVerificationCode(form.verificationCode)) {
    formError.value = { message: '验证码需要是六位数字。' }
    return
  }
  if (!form.displayName.trim()) {
    formError.value = { message: '请输入显示名称。' }
    return
  }
  const passwordError = validatePassword(form.password)
  if (passwordError) {
    formError.value = { message: passwordError }
    return
  }
  if (form.password !== form.confirmPassword) {
    formError.value = { message: '两次输入的密码不一致。' }
    return
  }

  isSubmitting.value = true
  try {
    await authStore.register({
      verificationId: challenge.id,
      verificationCode: form.verificationCode,
      displayName: form.displayName.trim(),
      password: form.password,
    })
    await router.replace({ name: 'workspace' })
  } catch (error) {
    formError.value = presentAuthError(error, '注册未能完成，请稍后重试。')
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <section class="auth-card" aria-labelledby="register-title">
    <header class="auth-card-header">
      <p>Create account</p>
      <h1 id="register-title">创建个人账户</h1>
      <span>首版使用邮箱验证码。验证码可在本地 Mailpit 收件箱中查看。</span>
    </header>

    <form class="auth-form" novalidate @submit.prevent="handleSubmit">
      <div class="auth-inline-action">
        <div class="auth-field">
          <label for="register-email">邮箱</label>
          <input
            id="register-email"
            v-model="form.email"
            type="email"
            autocomplete="email"
            inputmode="email"
            :disabled="verification.challenge.value !== null"
            required
          />
        </div>
        <button
          class="auth-secondary-button"
          type="button"
          :disabled="verification.isRequesting.value || verification.resendSeconds.value > 0"
          @click="sendCode"
        >
          <template v-if="verification.isRequesting.value">发送中…</template>
          <template v-else-if="verification.resendSeconds.value > 0">
            {{ verification.resendSeconds.value }} 秒后重发
          </template>
          <template v-else>{{ verification.challenge.value ? '重新发送' : '获取验证码' }}</template>
        </button>
      </div>

      <p v-if="verification.challenge.value" class="auth-success" role="status">
        验证码已经发送到 {{ form.email }}。
        <button class="auth-text-link" type="button" @click="changeEmail">更换邮箱</button>
      </p>
      <p v-if="verification.error.value" class="auth-alert" role="alert">
        {{ verification.error.value.message }}
        <span v-if="verification.error.value.requestId" class="auth-request-id">
          请求编号：{{ verification.error.value.requestId }}
        </span>
      </p>

      <div class="auth-field">
        <label for="register-code">验证码</label>
        <input
          id="register-code"
          v-model="form.verificationCode"
          type="text"
          inputmode="numeric"
          autocomplete="one-time-code"
          maxlength="6"
          required
        />
      </div>

      <div class="auth-field">
        <label for="register-name">显示名称</label>
        <input
          id="register-name"
          v-model="form.displayName"
          type="text"
          autocomplete="name"
          required
        />
      </div>

      <div class="auth-field">
        <label for="register-password">密码</label>
        <input
          id="register-password"
          v-model="form.password"
          type="password"
          autocomplete="new-password"
          required
        />
        <span class="auth-help">8～128 位，只能使用字母和数字，并包含大小写字母与数字。</span>
      </div>

      <div class="auth-field">
        <label for="register-confirm-password">确认密码</label>
        <input
          id="register-confirm-password"
          v-model="form.confirmPassword"
          type="password"
          autocomplete="new-password"
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
        {{ isSubmitting ? '正在创建…' : '创建账户' }}
      </button>
    </form>

    <nav class="auth-links" aria-label="其他认证操作">
      <span>已经有账户？</span>
      <RouterLink to="/login">返回登录</RouterLink>
    </nav>
  </section>
</template>

<style scoped>
.auth-text-link {
  padding: 0;
  border: 0;
  background: transparent;
  cursor: pointer;
  font-size: inherit;
}
</style>
