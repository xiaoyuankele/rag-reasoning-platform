import axios from 'axios'

/** 前端可以稳定处理的请求失败类别，不依赖后端日志文本。 */
export type ApiErrorKind =
  | 'timeout'
  | 'network'
  | 'client'
  | 'not-found'
  | 'conflict'
  | 'server'
  | 'invalid-response'
  | 'unknown'

/** 统一承载安全提示、HTTP 状态和错误类别的前端请求错误。 */
export class ApiError extends Error {
  readonly kind: ApiErrorKind
  readonly status?: number

  constructor(kind: ApiErrorKind, message: string, status?: number) {
    super(message)
    this.name = 'ApiError'
    this.kind = kind
    this.status = status
  }
}

interface BackendErrorPayload {
  error?: unknown
}

function readSafeClientMessage(data: unknown): string | undefined {
  if (typeof data !== 'object' || data === null) {
    return undefined
  }

  const message = (data as BackendErrorPayload).error
  return typeof message === 'string' && message.trim() ? message : undefined
}

/** 把 Axios、运行时和未知异常转换为界面可安全展示的统一错误。 */
export function toApiError(error: unknown): ApiError {
  if (error instanceof ApiError) {
    return error
  }

  if (!axios.isAxiosError(error)) {
    return new ApiError('unknown', '请求未能完成，请稍后重试。')
  }

  if (error.code === 'ECONNABORTED' || error.code === 'ETIMEDOUT') {
    return new ApiError('timeout', '请求超时，请确认服务状态后重试。')
  }

  if (!error.response) {
    return new ApiError('network', '无法连接后端服务，请确认服务已经启动。')
  }

  const status = error.response.status

  if (status >= 500) {
    return new ApiError('server', '后端服务暂时不可用，请稍后重试。', status)
  }

  const safeMessage = readSafeClientMessage(error.response.data)

  if (status === 404) {
    return new ApiError('not-found', safeMessage ?? '请求的资源不存在。', status)
  }

  if (status === 409) {
    return new ApiError('conflict', safeMessage ?? '当前状态暂时不能执行此操作。', status)
  }

  if (status >= 400) {
    return new ApiError('client', safeMessage ?? '请求内容不符合要求。', status)
  }

  return new ApiError('unknown', '请求未能完成，请稍后重试。', status)
}
