import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { AgentRun } from '../../entities/agent/model/agent-run'
import AgentRunPanel from './ui/AgentRunPanel.vue'

function createRun(status: AgentRun['status'] = 'succeeded'): AgentRun {
  const run: AgentRun = {
    id: 701,
    conversationId: 11,
    userMessageId: 201,
    assistantMessageId: 202,
    clientRunId: 'run-701',
    requestedDocumentId: 123,
    requestedResponseLanguage: 'auto',
    status,
    stepCount: status === 'succeeded' ? 3 : 0,
    maxSteps: 5,
    maxTotalTokens: 48_000,
    cancelable: status === 'queued',
    steps:
      status === 'succeeded'
        ? [
            {
              id: 801,
              agentRunId: 701,
              sequenceNo: 1,
              kind: 'tool_call',
              status: 'succeeded',
              toolName: 'search_documents',
              error: null,
              tokenUsage: { promptTokens: 10, completionTokens: 2, totalTokens: 12 },
              createdAt: new Date('2026-11-16T10:00:01Z'),
              startedAt: new Date('2026-11-16T10:00:01Z'),
              completedAt: new Date('2026-11-16T10:00:02Z'),
            },
            {
              id: 802,
              agentRunId: 701,
              sequenceNo: 2,
              kind: 'tool_call',
              status: 'succeeded',
              toolName: 'read_document_evidence',
              error: null,
              tokenUsage: { promptTokens: 8, completionTokens: 2, totalTokens: 10 },
              createdAt: new Date('2026-11-16T10:00:03Z'),
              startedAt: new Date('2026-11-16T10:00:03Z'),
              completedAt: new Date('2026-11-16T10:00:04Z'),
            },
            {
              id: 803,
              agentRunId: 701,
              sequenceNo: 3,
              kind: 'final',
              status: 'succeeded',
              toolName: null,
              error: null,
              tokenUsage: { promptTokens: 12, completionTokens: 6, totalTokens: 18 },
              createdAt: new Date('2026-11-16T10:00:05Z'),
              startedAt: new Date('2026-11-16T10:00:05Z'),
              completedAt: new Date('2026-11-16T10:00:06Z'),
            },
          ]
        : [],
    result:
      status === 'succeeded'
        ? {
            answer: '方案 B 更强调故障恢复。[1]',
            responseLanguage: 'zh',
            sources: [
              {
                citation: 1,
                evidenceRef: 'step-1:item-1',
                documentId: 123,
                chunkId: 456,
                title: '系统设计',
                pageStart: 12,
                pageEnd: 13,
                contentHash: 'a'.repeat(64),
              },
            ],
          }
        : null,
    error: null,
    tokenUsage:
      status === 'succeeded'
        ? { promptTokens: 30, completionTokens: 10, totalTokens: 40 }
        : { promptTokens: 0, completionTokens: 0, totalTokens: 0 },
    createdAt: new Date('2026-11-16T10:00:00Z'),
    updatedAt: new Date('2026-11-16T10:00:06Z'),
    startedAt: status === 'queued' ? null : new Date('2026-11-16T10:00:01Z'),
    completedAt: status === 'succeeded' ? new Date('2026-11-16T10:00:06Z') : null,
  }
  return run
}

describe('AgentRunPanel', () => {
  it('renders accessible status, safe local tool labels, answer, sources, and budget', () => {
    const run = createRun()
    Object.assign(run as unknown as Record<string, unknown>, {
      workerId: 'secret-worker',
      leaseToken: 'secret-lease',
      argumentsJson: 'secret-arguments',
      reasoningContent: 'secret-reasoning',
    })
    const wrapper = mount(AgentRunPanel, { props: { run, state: 'terminal' } })

    expect(wrapper.get('section').attributes('aria-labelledby')).toBe('agent-run-701-title')
    expect(wrapper.text()).toContain('已完成')
    expect(wrapper.text()).toContain('3 / 5 步')
    expect(wrapper.text()).toContain('检索文档')
    expect(wrapper.text()).toContain('读取证据')
    expect(wrapper.text()).toContain('生成最终回答')
    expect(wrapper.text()).toContain('方案 B 更强调故障恢复')
    expect(wrapper.text()).toContain('系统设计')
    expect(wrapper.text()).toContain('40 / 48000')
    expect(wrapper.text()).not.toContain('secret-worker')
    expect(wrapper.text()).not.toContain('secret-lease')
    expect(wrapper.text()).not.toContain('secret-arguments')
    expect(wrapper.text()).not.toContain('secret-reasoning')
  })

  it('shows cancel only from the DTO flag and emits the action', async () => {
    const queued = createRun('queued')
    const wrapper = mount(AgentRunPanel, { props: { run: queued, state: 'polling' } })
    await wrapper.get('.agent-cancel-button').trigger('click')
    expect(wrapper.emitted('cancel')).toHaveLength(1)

    await wrapper.setProps({ readOnly: true })
    expect(wrapper.get('.agent-cancel-button').attributes('disabled')).toBeDefined()
    await wrapper.setProps({ run: { ...queued, cancelable: false } })
    expect(wrapper.find('.agent-cancel-button').exists()).toBe(false)
  })

  it('renders processing copy and manual restore feedback without a cancel control', async () => {
    const wrapper = mount(AgentRunPanel, {
      props: {
        run: createRun('processing'),
        state: 'idle',
        errorMessage: '状态读取失败',
        requestId: 'agent-request-1',
        manualRestoreRequired: true,
      },
    })
    expect(wrapper.text()).toContain('执行中，当前不可取消')
    expect(wrapper.find('.agent-cancel-button').exists()).toBe(false)
    expect(wrapper.get('[role="alert"]').text()).toContain('请求编号：agent-request-1')
    await wrapper.get('.agent-restore-button').trigger('click')
    expect(wrapper.emitted('restore')).toHaveLength(1)
  })
})
