import { computed, onScopeDispose, ref } from 'vue'

/**
 * 将 Retry-After 转换成单一、可取消的客户端冷却计时器。
 * 它只控制当前浏览器交互，不承担后端配额或全局限流职责。
 */
export function useRetryCooldown() {
  const retryAt = ref<number | null>(null)
  const remainingSeconds = ref(0)
  let timer: ReturnType<typeof setTimeout> | null = null

  const isCoolingDown = computed(() => remainingSeconds.value > 0)

  function clearTimer(): void {
    if (timer !== null) clearTimeout(timer)
    timer = null
  }

  function updateRemaining(): void {
    if (retryAt.value === null) {
      remainingSeconds.value = 0
      return
    }

    const milliseconds = retryAt.value - Date.now()
    remainingSeconds.value = Math.max(0, Math.ceil(milliseconds / 1000))
    if (remainingSeconds.value === 0) {
      timer = null
      return
    }

    timer = setTimeout(updateRemaining, Math.min(1_000, Math.max(1, milliseconds)))
  }

  function start(seconds: number): void {
    clearTimer()
    const safeSeconds = Math.max(0, Math.ceil(seconds))
    retryAt.value = Date.now() + safeSeconds * 1_000
    updateRemaining()
  }

  function reset(): void {
    clearTimer()
    retryAt.value = null
    remainingSeconds.value = 0
  }

  onScopeDispose(reset)

  return { isCoolingDown, remainingSeconds, reset, retryAt, start }
}
