import { computed, onMounted, ref, shallowRef } from 'vue'
import type { SystemHealthSnapshot } from '../../../entities/system-health/model/system-health'
import { toApiError } from '../../../shared/api/api-error'
import { getSystemHealth } from '../api/get-system-health'

export type HealthRequestState = 'idle' | 'loading' | 'success' | 'error'

/** 管理一次健康检查的加载、成功和失败状态，不把临时请求状态放入 Pinia。 */
export function useSystemHealth() {
  const state = ref<HealthRequestState>('idle')
  const snapshot = shallowRef<SystemHealthSnapshot | null>(null)
  const errorMessage = ref('')

  const isLoading = computed(() => state.value === 'loading')

  async function refresh(): Promise<void> {
    if (isLoading.value) {
      return
    }

    state.value = 'loading'
    errorMessage.value = ''

    try {
      snapshot.value = await getSystemHealth()
      state.value = 'success'
    } catch (error) {
      snapshot.value = null
      errorMessage.value = toApiError(error).message
      state.value = 'error'
    }
  }

  onMounted(() => {
    void refresh()
  })

  return {
    errorMessage,
    isLoading,
    refresh,
    snapshot,
    state,
  }
}
