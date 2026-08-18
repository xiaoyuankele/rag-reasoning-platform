import axios from 'axios'

/** 前端可以稳定处理的请求失败类别，不依赖后端日志文本。 */
export type ApiErrorKind =
  | 'timeout'
  | 'network'
  | 'unauthorized'
  | 'rate-limited'
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
  readonly code?: string
  readonly requestId?: string
  readonly retryAfterSeconds?: number

  constructor(kind: ApiErrorKind, message: string, metadata: ApiErrorMetadata = {}) {
    super(message)
    this.name = 'ApiError'
    this.kind = kind
    this.status = metadata.status
    this.code = metadata.code
    this.requestId = metadata.requestId
    this.retryAfterSeconds = metadata.retryAfterSeconds
  }
}

interface BackendErrorPayload {
  error?: unknown
  code?: unknown
}

export interface ApiErrorMetadata {
  status?: number
  code?: string
  requestId?: string
  retryAfterSeconds?: number
}

function readSafeClientMessage(data: unknown): string | undefined {
  if (typeof data !== 'object' || data === null) {
    return undefined
  }

  const message = (data as BackendErrorPayload).error
  return typeof message === 'string' && message.trim() ? message : undefined
}

function readErrorCode(data: unknown): string | undefined {
  if (typeof data !== 'object' || data === null) return undefined

  const code = (data as BackendErrorPayload).code
  return typeof code === 'string' && code.trim() ? code : undefined
}

function readHeader(headers: unknown, name: string): string | undefined {
  if (typeof headers !== 'object' || headers === null) return undefined

  const headerContainer = headers as {
    get?: (headerName: string) => unknown
    [key: string]: unknown
  }
  const fromGetter = headerContainer.get?.(name)
  if (typeof fromGetter === 'string' && fromGetter.trim()) return fromGetter

  const lowerCaseName = name.toLowerCase()
  const directValue = headerContainer[lowerCaseName] ?? headerContainer[name]
  return typeof directValue === 'string' && directValue.trim() ? directValue : undefined
}

function readRetryAfterSeconds(headers: unknown): number | undefined {
  const value = readHeader(headers, 'retry-after')
  if (!value) return undefined

  const seconds = Number(value)
  if (Number.isFinite(seconds) && seconds >= 0) return Math.ceil(seconds)

  const retryAt = Date.parse(value)
  if (Number.isNaN(retryAt)) return undefined
  return Math.max(0, Math.ceil((retryAt - Date.now()) / 1000))
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
  const metadata: ApiErrorMetadata = {
    status,
    code: readErrorCode(error.response.data),
    requestId: readHeader(error.response.headers, 'x-request-id'),
    retryAfterSeconds: readRetryAfterSeconds(error.response.headers),
  }

  if (status >= 500) {
    return new ApiError('server', '后端服务暂时不可用，请稍后重试。', metadata)
  }

  const safeMessage = readSafeClientMessage(error.response.data)

  if (status === 401) {
    return new ApiError('unauthorized', safeMessage ?? '当前登录状态已失效。', metadata)
  }

  if (status === 429) {
    return new ApiError('rate-limited', safeMessage ?? '请求过于频繁，请稍后重试。', metadata)
  }

  if (status === 404) {
    return new ApiError('not-found', safeMessage ?? '请求的资源不存在。', metadata)
  }

  if (status === 409) {
    return new ApiError('conflict', safeMessage ?? '当前状态暂时不能执行此操作。', metadata)
  }

  if (status >= 400) {
    return new ApiError('client', safeMessage ?? '请求内容不符合要求。', metadata)
  }

  return new ApiError('unknown', '请求未能完成，请稍后重试。', metadata)
}
