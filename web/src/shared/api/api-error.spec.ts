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

    expect(result).toEqual(
      new ApiError('server', '后端服务暂时不可用，请稍后重试。', { status: 500 }),
    )
    expect(result.message).not.toContain('database')
  })

  it('保留稳定错误码、请求编号和限流等待时间', () => {
    const result = toApiError({
      isAxiosError: true,
      message: 'Request failed',
      response: {
        status: 429,
        data: {
          error: 'verification requests are temporarily limited',
          code: 'verification_request_throttled',
        },
        headers: {
          'x-request-id': 'request-42',
          'retry-after': '37',
        },
      },
    })

    expect(result).toMatchObject({
      kind: 'rate-limited',
      status: 429,
      code: 'verification_request_throttled',
      requestId: 'request-42',
      retryAfterSeconds: 37,
    })
  })

  it('把认证失效与普通凭据错误都保留为可区分的 401 code', () => {
    const result = toApiError({
      isAxiosError: true,
      message: 'Request failed',
      response: {
        status: 401,
        data: { error: 'authentication is required', code: 'authentication_required' },
        headers: {},
      },
    })

    expect(result).toMatchObject({
      kind: 'unauthorized',
      status: 401,
      code: 'authentication_required',
    })
  })
})
