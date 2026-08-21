import type {
  AnswerResponseLanguage,
  GroundedAnswer,
  GroundedAnswerSource,
} from '../../../entities/answer/model/grounded-answer'
import { ApiError } from '../../../shared/api/api-error'
import { httpClient } from '../../../shared/api/http-client'

export interface AskGroundedQuestionParams {
  query: string
  documentId?: number
  topK: number
  responseLanguage: AnswerResponseLanguage
}

interface AnswerSourceDto {
  citation: number
  chunk_id: number
  document_id: number
  chunk_index: number
  title: string | null
  original_name: string
  page_start: number | null
  page_end: number | null
  similarity: number
}

interface AnswerUsageDto {
  prompt_tokens: number
  completion_tokens: number
  total_tokens: number
}

interface AnswerResponseDto {
  query: string
  answer: string
  response_language: AnswerResponseLanguage
  sources: AnswerSourceDto[]
  usage: AnswerUsageDto
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null
}

function isInteger(value: unknown, minimum = 0): value is number {
  return Number.isSafeInteger(value) && (value as number) >= minimum
}

function isNullablePositiveInteger(value: unknown): value is number | null {
  return value === null || isInteger(value, 1)
}

function isAnswerSourceDto(value: unknown): value is AnswerSourceDto {
  if (!isRecord(value)) return false

  return (
    isInteger(value.citation, 1) &&
    isInteger(value.chunk_id, 1) &&
    isInteger(value.document_id, 1) &&
    isInteger(value.chunk_index) &&
    (value.title === null || typeof value.title === 'string') &&
    typeof value.original_name === 'string' &&
    value.original_name.trim().length > 0 &&
    isNullablePositiveInteger(value.page_start) &&
    isNullablePositiveInteger(value.page_end) &&
    typeof value.similarity === 'number' &&
    Number.isFinite(value.similarity) &&
    value.similarity >= -1 &&
    value.similarity <= 1
  )
}

function isAnswerUsageDto(value: unknown): value is AnswerUsageDto {
  if (!isRecord(value)) return false
  return (
    isInteger(value.prompt_tokens) &&
    isInteger(value.completion_tokens) &&
    isInteger(value.total_tokens) &&
    value.total_tokens === value.prompt_tokens + value.completion_tokens
  )
}

function isAnswerResponseDto(value: unknown): value is AnswerResponseDto {
  if (!isRecord(value)) return false
  if (
    typeof value.query !== 'string' ||
    value.query.trim().length === 0 ||
    typeof value.answer !== 'string' ||
    value.answer.trim().length === 0 ||
    !['auto', 'zh', 'en'].includes(value.response_language as string) ||
    !Array.isArray(value.sources) ||
    !value.sources.every(isAnswerSourceDto) ||
    !isAnswerUsageDto(value.usage)
  ) {
    return false
  }

  return value.sources.every((source, index) => source.citation === index + 1)
}

function mapSource(source: AnswerSourceDto): GroundedAnswerSource {
  return {
    citation: source.citation,
    chunkId: source.chunk_id,
    documentId: source.document_id,
    chunkIndex: source.chunk_index,
    title: source.title,
    originalName: source.original_name,
    pageStart: source.page_start,
    pageEnd: source.page_end,
    similarity: source.similarity,
  }
}

export function mapAnswerResponse(data: unknown): GroundedAnswer {
  if (!isAnswerResponseDto(data)) {
    throw new ApiError('invalid-response', '后端问答响应不符合约定。')
  }

  return {
    query: data.query,
    answer: data.answer,
    responseLanguage: data.response_language,
    sources: data.sources.map(mapSource),
    usage: {
      promptTokens: data.usage.prompt_tokens,
      completionTokens: data.usage.completion_tokens,
      totalTokens: data.usage.total_tokens,
    },
  }
}

/** 显式发起一次可能产生远程模型费用的带来源问答。 */
export async function askGroundedQuestion(
  params: AskGroundedQuestionParams,
  signal?: AbortSignal,
): Promise<GroundedAnswer> {
  const response = await httpClient.post<unknown>(
    '/answers',
    {
      query: params.query,
      document_id: params.documentId,
      top_k: params.topK,
      response_language: params.responseLanguage,
    },
    {
      signal,
      // 后端 Generation 默认允许 60 秒；基础 Client 的 10 秒超时不适用于远程问答。
      timeout: 70_000,
    },
  )
  return mapAnswerResponse(response.data)
}
