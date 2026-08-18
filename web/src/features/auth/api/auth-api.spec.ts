import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapAuthSession, mapCurrentUser, mapVerificationChallenge } from './auth-api'

const userDto = {
  id: 17,
  email: 'learner@example.com',
  phone: null,
  display_name: 'learner',
  status: 'active',
  created_at: '2026-08-17T08:04:16Z',
}

describe('auth API DTO mapping', () => {
  it('转换验证码挑战并保留服务端时间', () => {
    expect(
      mapVerificationChallenge({
        verification_id: 21,
        expires_at: '2026-08-17T08:10:00Z',
        resend_after: '2026-08-17T08:01:00Z',
      }),
    ).toEqual({
      id: 21,
      expiresAt: '2026-08-17T08:10:00Z',
      resendAfter: '2026-08-17T08:01:00Z',
    })
  })

  it('转换注册和登录共用的 Session 响应', () => {
    expect(
      mapAuthSession({
        user: userDto,
        session_expires_at: '2026-08-24T08:04:16Z',
      }),
    ).toMatchObject({
      user: { id: 17, displayName: 'learner' },
      sessionExpiresAt: '2026-08-24T08:04:16Z',
    })
  })

  it('转换 /users/me 响应并拒绝无 user 的响应', () => {
    expect(mapCurrentUser({ user: userDto })).toMatchObject({ id: 17 })
    expect(() => mapCurrentUser({})).toThrow(
      new ApiError('invalid-response', '后端用户响应不符合约定。'),
    )
  })
})
