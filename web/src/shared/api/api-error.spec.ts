import { describe, expect, it } from 'vitest'
import { ApiError, toApiError } from './api-error'

describe('toApiError', () => {
  it('把无响应的 Axios 异常转换为网络错误', () => {
    const result = toApiError({
      isAxiosError: true,
      message: 'Network Error',
    })

    expect(result).toMatchObject({
      kind: 'network',
      message: '无法连接后端服务，请确认服务已经启动。',
    })
  })

  it('不会把服务端内部错误详情直接展示给用户', () => {
    const result = toApiError({
      isAxiosError: true,
      message: 'Request failed',
      response: {
        status: 500,
        data: { error: 'database password leaked in internal error' },
      },
    })

    expect(result).toEqual(new ApiError('server', '后端服务暂时不可用，请稍后重试。', 500))
    expect(result.message).not.toContain('database')
  })
})
