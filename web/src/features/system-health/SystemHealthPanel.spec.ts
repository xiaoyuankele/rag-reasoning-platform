import { flushPromises, mount } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../shared/api/api-error'
import { getSystemHealth } from './api/get-system-health'
import SystemHealthPanel from './ui/SystemHealthPanel.vue'

vi.mock('./api/get-system-health', () => ({
  getSystemHealth: vi.fn(),
}))

const getSystemHealthMock = vi.mocked(getSystemHealth)

afterEach(() => {
  vi.clearAllMocks()
})

describe('SystemHealthPanel', () => {
  it('展示统一健康接口返回的成功状态', async () => {
    getSystemHealthMock.mockResolvedValue({
      status: 'online',
      checkedAt: new Date('2026-08-14T08:00:00Z'),
    })

    const wrapper = mount(SystemHealthPanel)
    await flushPromises()

    expect(wrapper.text()).toContain('后端运行正常')
    expect(getSystemHealthMock).toHaveBeenCalledOnce()
    wrapper.unmount()
  })

  it('展示安全错误并允许用户重试', async () => {
    getSystemHealthMock.mockRejectedValueOnce(
      new ApiError('network', '无法连接后端服务，请确认服务已经启动。'),
    )
    getSystemHealthMock.mockResolvedValueOnce({
      status: 'online',
      checkedAt: new Date('2026-08-14T08:00:00Z'),
    })

    const wrapper = mount(SystemHealthPanel)
    await flushPromises()

    expect(wrapper.get('[role="alert"]').text()).toContain('无法连接后端服务')

    await wrapper.get('button').trigger('click')
    await flushPromises()

    expect(wrapper.text()).toContain('后端运行正常')
    expect(getSystemHealthMock).toHaveBeenCalledTimes(2)
    wrapper.unmount()
  })
})
