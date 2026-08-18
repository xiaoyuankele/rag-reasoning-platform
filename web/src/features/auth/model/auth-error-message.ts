import { ApiError } from '../../../shared/api/api-error'

const codeMessages: Record<string, string> = {
  invalid_verification_request: '邮箱或验证码用途不符合要求。',
  invalid_auth_request: '注册信息或密码不符合要求。',
  invalid_password_reset_request: '验证码或新密码不符合要求。',
  verification_code_invalid: '验证码不正确，请重新检查。',
  verification_code_expired: '验证码已过期，请重新获取。',
  verification_attempts_exceeded: '验证码尝试次数过多，请重新获取。',
  invalid_credentials: '邮箱或密码不正确。',
  contact_already_registered: '该邮箱已经注册，请直接登录或找回密码。',
  verification_channel_unavailable: '验证码邮件服务暂时不可用。',
  verification_request_throttled: '验证码请求过于频繁，请稍后再试。',
  auth_request_throttled: '认证请求过于频繁，请稍后再试。',
  request_origin_not_allowed: '当前请求来源不被允许，请从正常页面重新操作。',
}

export interface AuthErrorPresentation {
  message: string
  requestId?: string
  retryAfterSeconds?: number
}

export function presentAuthError(error: unknown, fallbackMessage: string): AuthErrorPresentation {
  if (!(error instanceof ApiError)) return { message: fallbackMessage }

  return {
    message: (error.code && codeMessages[error.code]) || error.message || fallbackMessage,
    requestId: error.requestId,
    retryAfterSeconds: error.retryAfterSeconds,
  }
}
