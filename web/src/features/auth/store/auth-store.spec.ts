import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiError } from '../../../shared/api/api-error'

const authApi = vi.hoisted(() => ({
  getCurrentUser: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  register: vi.fn(),
  resetPassword: vi.fn(),
}))

vi.mock('../api/auth-api', () => authApi)

import { useAuthStore } from './auth-store'

const user = {
  id: 17,
  email: 'learner@example.com',
  phone: null,
  displayName: 'learner',
  status: 'active' as const,
  createdAt: '2026-08-17T08:04:16Z',
}

describe('auth store', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    vi.clearAllMocks()
  })

  it('通过 /users/me 从 unknown 恢复为 authenticated', async () => {
    authApi.getCurrentUser.mockResolvedValue(user)
    const store = useAuthStore()

    await store.restoreSession()

    expect(store.status).toBe('authenticated')
    expect(store.user).toEqual(user)
    expect(store.isAuthenticated).toBe(true)
  })

  it('把 authentication_required 恢复为 anonymous 而不是启动错误', async () => {
    authApi.getCurrentUser.mockRejectedValue(
      new ApiError('unauthorized', 'authentication is required', {
        status: 401,
        code: 'authentication_required',
      }),
    )
    const store = useAuthStore()

    await store.restoreSession()

    expect(store.status).toBe('anonymous')
    expect(store.restoreError).toBeNull()
  })

  it('登录成功后保存公开用户和 Session 过期时间', async () => {
    authApi.login.mockResolvedValue({
      user,
      sessionExpiresAt: '2026-08-24T08:04:16Z',
    })
    const store = useAuthStore()

    await store.login({ identifier: 'learner@example.com', password: 'Password123' })

    expect(store.status).toBe('authenticated')
    expect(store.sessionExpiresAt).toBe('2026-08-24T08:04:16Z')
  })

  it('密码重置成功后清空当前身份', async () => {
    authApi.resetPassword.mockResolvedValue(undefined)
    const store = useAuthStore()
    store.$patch({ status: 'authenticated', user })

    await store.resetPassword({
      verificationId: 29,
      verificationCode: '725184',
      newPassword: 'Changed123',
    })

    expect(store.status).toBe('anonymous')
    expect(store.user).toBeNull()
  })
})
