import type { SystemHealthSnapshot } from '../../../entities/system-health/model/system-health'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

interface HealthResponseDto {
  status: string
}

function isHealthResponseDto(data: unknown): data is HealthResponseDto {
  return typeof data === 'object' && data !== null && 'status' in data
}

/** 请求后端 GET /health，并把响应 DTO 转换为前端健康快照。 */
export async function getSystemHealth(): Promise<SystemHealthSnapshot> {
  const response = await httpClient.get<unknown>('/health')

  if (!isHealthResponseDto(response.data) || response.data.status !== 'ok') {
    throw new ApiError('invalid-response', '后端健康检查响应不符合约定。')
  }

  return {
    status: 'online',
    checkedAt: new Date(),
  }
}
