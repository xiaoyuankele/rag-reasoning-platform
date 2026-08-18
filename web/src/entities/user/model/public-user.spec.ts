import { describe, expect, it } from 'vitest'
import { ApiError } from '../../../shared/api/api-error'
import { mapPublicUser } from './public-user'

describe('mapPublicUser', () => {
  it('把公开用户 DTO 转换为前端模型', () => {
    expect(
      mapPublicUser({
        id: 17,
        email: 'learner@example.com',
        phone: null,
        display_name: 'learner',
        status: 'active',
        created_at: '2026-08-17T08:04:16Z',
      }),
    ).toEqual({
      id: 17,
      email: 'learner@example.com',
      phone: null,
      displayName: 'learner',
      status: 'active',
      createdAt: '2026-08-17T08:04:16Z',
    })
  })

  it('拒绝没有已验证联系方式的异常用户响应', () => {
    expect(() =>
      mapPublicUser({
        id: 17,
        email: null,
        phone: null,
        display_name: 'learner',
        status: 'active',
        created_at: '2026-08-17T08:04:16Z',
      }),
    ).toThrow(new ApiError('invalid-response', '后端用户响应不符合约定。'))
  })
})
