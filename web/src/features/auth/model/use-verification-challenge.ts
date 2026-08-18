import { computed, onUnmounted, ref } from 'vue'
import {
  requestVerificationCode,
  type VerificationChallenge,
  type VerificationPurpose,
} from '../api/auth-api'
import { presentAuthError, type AuthErrorPresentation } from './auth-error-message'

/** 管理验证码挑战、服务端重发时间与 429 冷却，不保存验证码明文。 */
export function useVerificationChallenge(purpose: VerificationPurpose) {
  const challenge = ref<VerificationChallenge | null>(null)
  const isRequesting = ref(false)
  const error = ref<AuthErrorPresentation | null>(null)
  const now = ref(Date.now())
  const throttledUntil = ref(0)
  const timer = window.setInterval(() => {
    now.value = Date.now()
  }, 1000)

  onUnmounted(() => window.clearInterval(timer))

  const resendAvailableAt = computed(() => {
    const challengeTime = challenge.value ? Date.parse(challenge.value.resendAfter) : 0
    return Math.max(challengeTime, throttledUntil.value)
  })

  const resendSeconds = computed(() =>
    Math.max(0, Math.ceil((resendAvailableAt.value - now.value) / 1000)),
  )

  async function request(email: string): Promise<boolean> {
    isRequesting.value = true
    error.value = null
    try {
      challenge.value = await requestVerificationCode(email.trim(), purpose)
      now.value = Date.now()
      return true
    } catch (requestError) {
      const presentation = presentAuthError(requestError, '验证码暂时无法发送，请稍后重试。')
      error.value = presentation
      if (presentation.retryAfterSeconds !== undefined) {
        throttledUntil.value = Date.now() + presentation.retryAfterSeconds * 1000
      }
      return false
    } finally {
      isRequesting.value = false
    }
  }

  function clear(): void {
    challenge.value = null
    error.value = null
    throttledUntil.value = 0
  }

  return {
    challenge,
    isRequesting,
    error,
    resendSeconds,
    request,
    clear,
  }
}
