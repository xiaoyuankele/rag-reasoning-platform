import { ApiError } from '../../../shared/api/api-error'

export interface PublicUser {
  id: number
  email: string | null
  phone: string | null
  displayName: string
  status: 'active'
  createdAt: string
}

export interface PublicUserDto {
  id: number
  email: string | null
  phone: string | null
  display_name: string
  status: 'active'
  created_at: string
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isNullableString(value: unknown): value is string | null {
  return value === null || typeof value === 'string'
}

function isTimestamp(value: unknown): value is string {
  return typeof value === 'string' && !Number.isNaN(Date.parse(value))
}

export function isPublicUserDto(value: unknown): value is PublicUserDto {
  if (!isRecord(value)) return false

  return (
    typeof value.id === 'number' &&
    Number.isSafeInteger(value.id) &&
    value.id > 0 &&
    isNullableString(value.email) &&
    isNullableString(value.phone) &&
    (value.email !== null || value.phone !== null) &&
    typeof value.display_name === 'string' &&
    value.display_name.trim().length > 0 &&
    value.status === 'active' &&
    isTimestamp(value.created_at)
  )
}

/** 校验公开 User DTO，并隔离后端 snake_case 字段。 */
export function mapPublicUser(data: unknown): PublicUser {
  if (!isPublicUserDto(data)) {
    throw new ApiError('invalid-response', '后端用户响应不符合约定。')
  }

  return {
    id: data.id,
    email: data.email,
    phone: data.phone,
    displayName: data.display_name,
    status: data.status,
    createdAt: data.created_at,
  }
}
