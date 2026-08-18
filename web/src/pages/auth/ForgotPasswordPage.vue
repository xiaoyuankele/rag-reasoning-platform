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
const verification = useVerificationChallenge('password_reset')
const form = reactive({
  email: '',
  verificationCode: '',
  newPassword: '',
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
    formError.value = { message: '请先获取密码重置验证码。' }
    return
  }
  if (!isValidVerificationCode(form.verificationCode)) {
    formError.value = { message: '验证码需要是六位数字。' }
    return
  }
  const passwordError = validatePassword(form.newPassword)
  if (passwordError) {
    formError.value = { message: passwordError }
    return
  }
  if (form.newPassword !== form.confirmPassword) {
    formError.value = { message: '两次输入的新密码不一致。' }
    return
  }

  isSubmitting.value = true
  try {
    await authStore.resetPassword({
      verificationId: challenge.id,
      verificationCode: form.verificationCode,
      newPassword: form.newPassword,
    })
    await router.replace({ name: 'login', query: { reset: 'success' } })
  } catch (error) {
    formError.value = presentAuthError(error, '密码重置未能完成，请稍后重试。')
  } finally {
    isSubmitting.value = false
  }
}
</script>

<template>
  <section class="auth-card" aria-labelledby="reset-title">
    <header class="auth-card-header">
      <p>Reset password</p>
      <h1 id="reset-title">重置密码</h1>
      <span>验证码只用于本次密码重置；成功后所有旧登录状态都会失效。</span>
    </header>

    <form class="auth-form" novalidate @submit.prevent="handleSubmit">
      <div class="auth-inline-action">
        <div class="auth-field">
          <label for="reset-email">账户邮箱</label>
          <input
            id="reset-email"
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
        <label for="reset-code">验证码</label>
        <input
          id="reset-code"
          v-model="form.verificationCode"
          type="text"
          inputmode="numeric"
          autocomplete="one-time-code"
          maxlength="6"
          required
        />
      </div>

      <div class="auth-field">
        <label for="reset-password">新密码</label>
        <input
          id="reset-password"
          v-model="form.newPassword"
          type="password"
          autocomplete="new-password"
          required
        />
        <span class="auth-help">8～128 位，只能使用字母和数字，并包含大小写字母与数字。</span>
      </div>

      <div class="auth-field">
        <label for="reset-confirm-password">确认新密码</label>
        <input
          id="reset-confirm-password"
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
        {{ isSubmitting ? '正在重置…' : '确认重置密码' }}
      </button>
    </form>

    <nav class="auth-links" aria-label="其他认证操作">
      <span>想起密码了？</span>
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
