import { computed, ref } from 'vue'
import { defineStore } from 'pinia'
import type { PublicUser } from '../../../entities/user/model/public-user'
import { ApiError } from '../../../shared/api/api-error'
import {
  clearAllPrivateSessionStorage,
  clearPrivateSessionStorageExcept,
} from '../../../shared/storage/private-session-storage'
import {
  getCurrentUser,
  login as loginRequest,
  logout as logoutRequest,
  register as registerRequest,
  resetPassword as resetPasswordRequest,
  type LoginInput,
  type RegisterInput,
  type ResetPasswordInput,
} from '../api/auth-api'

export type AuthStatus = 'unknown' | 'authenticated' | 'anonymous'

export const useAuthStore = defineStore('auth', () => {
  const status = ref<AuthStatus>('unknown')
  const user = ref<PublicUser | null>(null)
  const sessionExpiresAt = ref<string | null>(null)
  const restoreError = ref<string | null>(null)
  let restorePromise: Promise<void> | undefined

  const isAuthenticated = computed(() => status.value === 'authenticated' && user.value !== null)

  function markAuthenticated(nextUser: PublicUser, expiresAt: string | null): void {
    clearPrivateSessionStorageExcept(nextUser.id)
    user.value = nextUser
    sessionExpiresAt.value = expiresAt
    restoreError.value = null
    status.value = 'authenticated'
  }

  function markAnonymous(): void {
    clearAllPrivateSessionStorage()
    user.value = null
    sessionExpiresAt.value = null
    status.value = 'anonymous'
  }

  async function restoreSession(): Promise<void> {
    if (status.value !== 'unknown') return
    if (restorePromise) return restorePromise

    restorePromise = (async () => {
      try {
        const currentUser = await getCurrentUser()
        markAuthenticated(currentUser, null)
      } catch (error) {
        markAnonymous()
        if (!(error instanceof ApiError && error.code === 'authentication_required')) {
          restoreError.value = error instanceof ApiError ? error.message : '暂时无法确认登录状态。'
        }
      } finally {
        restorePromise = undefined
      }
    })()

    return restorePromise
  }

  async function login(input: LoginInput): Promise<void> {
    const session = await loginRequest(input)
    markAuthenticated(session.user, session.sessionExpiresAt)
  }

  async function register(input: RegisterInput): Promise<void> {
    const session = await registerRequest(input)
    markAuthenticated(session.user, session.sessionExpiresAt)
  }

  async function logout(): Promise<void> {
    await logoutRequest()
    markAnonymous()
  }

  async function resetPassword(input: ResetPasswordInput): Promise<void> {
    await resetPasswordRequest(input)
    markAnonymous()
  }

  return {
    status,
    user,
    sessionExpiresAt,
    restoreError,
    isAuthenticated,
    markAnonymous,
    restoreSession,
    login,
    register,
    logout,
    resetPassword,
  }
})
