import axios from 'axios'
import { toApiError } from './api-error'

const apiBaseUrl = import.meta.env.VITE_API_BASE_URL?.trim() || '/api'

/** 项目唯一的基础 HTTP Client，统一管理基础地址、超时和错误转换。 */
export const httpClient = axios.create({
  baseURL: apiBaseUrl,
  timeout: 10_000,
  headers: {
    Accept: 'application/json',
  },
})

httpClient.interceptors.response.use(
  (response) => response,
  (error: unknown) => Promise.reject(toApiError(error)),
)
