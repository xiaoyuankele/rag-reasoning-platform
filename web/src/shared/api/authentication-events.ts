import type { ApiError } from './api-error'

type AuthenticationRequiredHandler = (error: ApiError) => void

let authenticationRequiredHandler: AuthenticationRequiredHandler | undefined

/** 由应用入口注入会话失效处理，避免共享 HTTP 层反向依赖 Pinia 或 Router。 */
export function setAuthenticationRequiredHandler(
  handler: AuthenticationRequiredHandler,
): () => void {
  authenticationRequiredHandler = handler

  return () => {
    if (authenticationRequiredHandler === handler) authenticationRequiredHandler = undefined
  }
}

export function notifyAuthenticationRequired(error: ApiError): void {
  authenticationRequiredHandler?.(error)
}
