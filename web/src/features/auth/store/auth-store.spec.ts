import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { ApiError } from '../../../shared/api/api-error'
import { privateSessionStorageKey } from '../../../shared/storage/private-session-storage'

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
    sessionStorage.clear()
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
    sessionStorage.setItem(privateSessionStorageKey(17, 'search'), 'retain')
    sessionStorage.setItem(privateSessionStorageKey(18, 'search'), 'remove')

    await store.login({ identifier: 'learner@example.com', password: 'Password123' })

    expect(store.status).toBe('authenticated')
    expect(store.sessionExpiresAt).toBe('2026-08-24T08:04:16Z')
    expect(sessionStorage.getItem(privateSessionStorageKey(17, 'search'))).toBe('retain')
    expect(sessionStorage.getItem(privateSessionStorageKey(18, 'search'))).toBeNull()
  })

  it('退出或认证失效时清除当前标签页的全部用户私有缓存', async () => {
    authApi.logout.mockResolvedValue(undefined)
    const store = useAuthStore()
    store.$patch({ status: 'authenticated', user })
    sessionStorage.setItem(privateSessionStorageKey(17, 'search'), 'private result')
    sessionStorage.setItem('unrelated:key', 'keep')

    await store.logout()

    expect(store.status).toBe('anonymous')
    expect(sessionStorage.getItem(privateSessionStorageKey(17, 'search'))).toBeNull()
    expect(sessionStorage.getItem('unrelated:key')).toBe('keep')
  })

  it('密码重置成功后清空当前身份', async () => {
    authApi.resetPassword.mockResolvedValue(undefined)
    const store = useAuthStore()
    store.$patch({ status: 'authenticated', user })
    sessionStorage.setItem(privateSessionStorageKey(17, 'search'), 'private result')

    await store.resetPassword({
      verificationId: 29,
      verificationCode: '725184',
      newPassword: 'Changed123',
    })

    expect(store.status).toBe('anonymous')
    expect(store.user).toBeNull()
    expect(sessionStorage.getItem(privateSessionStorageKey(17, 'search'))).toBeNull()
  })
})
