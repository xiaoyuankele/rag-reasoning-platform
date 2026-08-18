import { mapPublicUser, type PublicUser } from '../../../entities/user/model/public-user'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

export type VerificationPurpose = 'register' | 'password_reset'

export interface VerificationChallenge {
  id: number
  expiresAt: string
  resendAfter: string
}

export interface AuthSession {
  user: PublicUser
  sessionExpiresAt: string
}

export interface RegisterInput {
  verificationId: number
  verificationCode: string
  displayName: string
  password: string
}

export interface LoginInput {
  identifier: string
  password: string
}

export interface ResetPasswordInput {
  verificationId: number
  verificationCode: string
  newPassword: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
}

function isTimestamp(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

export function mapVerificationChallenge(data: unknown): VerificationChallenge {
  if (
    !isRecord(data) ||
    !isPositiveInteger(data.verification_id) ||
    !isTimestamp(data.expires_at) ||
    !isTimestamp(data.resend_after)
  ) {
    throw new ApiError('invalid-response', '后端验证码响应不符合约定。')
  }

  return {
    id: data.verification_id,
    expiresAt: data.expires_at,
    resendAfter: data.resend_after,
  }
}

export function mapAuthSession(data: unknown): AuthSession {
  if (!isRecord(data) || !isTimestamp(data.session_expires_at)) {
    throw new ApiError('invalid-response', '后端认证响应不符合约定。')
  }

  return {
    user: mapPublicUser(data.user),
    sessionExpiresAt: data.session_expires_at,
  }
}

export function mapCurrentUser(data: unknown): PublicUser {
  if (!isRecord(data)) {
    throw new ApiError('invalid-response', '后端当前用户响应不符合约定。')
  }
  return mapPublicUser(data.user)
}

/** 申请邮箱验证码；明文验证码只由邮件渠道交付，不会出现在响应中。 */
export async function requestVerificationCode(
  destination: string,
  purpose: VerificationPurpose,
): Promise<VerificationChallenge> {
  const response = await httpClient.post<unknown>('/auth/verification-codes', {
    channel: 'email',
    destination,
    purpose,
  })
  return mapVerificationChallenge(response.data)
}

export async function register(input: RegisterInput): Promise<AuthSession> {
  const response = await httpClient.post<unknown>('/auth/register', {
    verification_id: input.verificationId,
    verification_code: input.verificationCode,
    display_name: input.displayName,
    password: input.password,
  })
  return mapAuthSession(response.data)
}

export async function login(input: LoginInput): Promise<AuthSession> {
  const response = await httpClient.post<unknown>('/auth/login', input)
  return mapAuthSession(response.data)
}

export async function getCurrentUser(): Promise<PublicUser> {
  const response = await httpClient.get<unknown>('/users/me')
  return mapCurrentUser(response.data)
}

export async function logout(): Promise<void> {
  await httpClient.post('/auth/logout')
}

export async function resetPassword(input: ResetPasswordInput): Promise<void> {
  await httpClient.post('/auth/password-reset', {
    verification_id: input.verificationId,
    verification_code: input.verificationCode,
    new_password: input.newPassword,
  })
}
