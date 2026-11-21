import { effectScope } from 'vue'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { useRetryCooldown } from './use-retry-cooldown'

afterEach(() => vi.useRealTimers())

describe('useRetryCooldown', () => {
  it('只保留一个计时器并在到期后开放操作', async () => {
    vi.useFakeTimers()
    vi.setSystemTime(new Date('2026-08-23T00:00:00Z'))
    const scope = effectScope()
    const cooldown = scope.run(() => useRetryCooldown())!

    cooldown.start(2)
    expect(cooldown.remainingSeconds.value).toBe(2)
    expect(cooldown.isCoolingDown.value).toBe(true)
    expect(vi.getTimerCount()).toBe(1)

    cooldown.start(3)
    expect(cooldown.remainingSeconds.value).toBe(3)
    expect(vi.getTimerCount()).toBe(1)

    await vi.advanceTimersByTimeAsync(3_000)
    expect(cooldown.remainingSeconds.value).toBe(0)
    expect(cooldown.isCoolingDown.value).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    scope.stop()
  })

  it('作用域销毁时取消等待计时器', () => {
    vi.useFakeTimers()
    const scope = effectScope()
    const cooldown = scope.run(() => useRetryCooldown())!
    cooldown.start(5)

    scope.stop()

    expect(vi.getTimerCount()).toBe(0)
  })
})
