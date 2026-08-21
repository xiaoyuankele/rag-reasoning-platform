import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import type { GroundedAnswer } from '../../../entities/answer/model/grounded-answer'
import { toApiError, type ApiError } from '../../../shared/api/api-error'
import { askGroundedQuestion, type AskGroundedQuestionParams } from '../api/answer-api'

export type GroundedAnswerState = 'idle' | 'loading' | 'success' | 'insufficient-evidence' | 'error'

function answerErrorMessage(error: ApiError): string {
  if (error.kind === 'conflict') {
    return '所选范围的文档向量尚未就绪。请先在“向量化”页面完成任务后再提问。'
  }
  if (error.kind === 'not-found') {
    return error.message === 'document not found'
      ? '所选文档不存在或当前账户不可访问，请重新选择范围。'
      : '当前后端没有开放问答接口。请确认 ANSWER_ENABLED 已经显式启用。'
  }
  if (error.kind === 'timeout') {
    return '回答生成超时。远程模型可能仍然繁忙，请稍后重试。'
  }
  if (error.status === 503) {
    return '问答服务暂时不可用，可能尚未开启、额度不足或远程模型繁忙。'
  }
  if (error.status === 502) {
    return '远程模型返回了无法使用的响应，请稍后重试。'
  }
  if (error.kind === 'invalid-response') return error.message
  if (error.kind === 'client') return '问答参数不符合后端要求，请检查问题、语言和证据数量。'
  return error.message
}

/** 管理单次带来源问答；新问题会取消旧请求，页面卸载时也会停止等待。 */
export function useGroundedAnswer() {
  const state = ref<GroundedAnswerState>('idle')
  const result = shallowRef<GroundedAnswer | null>(null)
  const errorMessage = ref('')
  const requestId = ref<string | null>(null)
  let activeController: AbortController | null = null
  let lastParams: AskGroundedQuestionParams | null = null

  const isLoading = computed(() => state.value === 'loading')
  const canRetry = computed(() => state.value === 'error' && lastParams !== null)

  async function ask(params: AskGroundedQuestionParams): Promise<void> {
    activeController?.abort()
    const requestController = new AbortController()
    activeController = requestController
    lastParams = { ...params }
    result.value = null
    errorMessage.value = ''
    requestId.value = null
    state.value = 'loading'

    try {
      const nextResult = await askGroundedQuestion(params, requestController.signal)
      if (requestController.signal.aborted) return

      result.value = nextResult
      state.value = nextResult.sources.length === 0 ? 'insufficient-evidence' : 'success'
    } catch (error) {
      if (requestController.signal.aborted) return

      const apiError = toApiError(error)
      errorMessage.value = answerErrorMessage(apiError)
      requestId.value = apiError.requestId ?? null
      state.value = 'error'
    } finally {
      if (activeController === requestController) activeController = null
    }
  }

  async function retry(): Promise<void> {
    if (!lastParams) return
    await ask(lastParams)
  }

  function reset(): void {
    activeController?.abort()
    activeController = null
    lastParams = null
    result.value = null
    errorMessage.value = ''
    requestId.value = null
    state.value = 'idle'
  }

  onScopeDispose(() => activeController?.abort())

  return {
    ask,
    canRetry,
    errorMessage,
    isLoading,
    requestId,
    reset,
    result,
    retry,
    state,
  }
}
