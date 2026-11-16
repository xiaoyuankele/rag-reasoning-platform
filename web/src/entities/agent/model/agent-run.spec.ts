import { describe, expect, it } from 'vitest'
import type { AgentRun } from './agent-run'
import {
  agentRunStatusLabels,
  agentStepStatusLabels,
  agentToolLabels,
  canCancelAgentRun,
  isAgentRunActive,
  isAgentRunTerminal,
} from './agent-run'

const queuedRun = {
  status: 'queued',
  cancelable: true,
} as AgentRun

describe('Agent Run entity', () => {
  it('classifies active and terminal statuses', () => {
    expect(isAgentRunActive('queued')).toBe(true)
    expect(isAgentRunActive('processing')).toBe(true)
    expect(isAgentRunActive('succeeded')).toBe(false)
    expect(isAgentRunTerminal('succeeded')).toBe(true)
    expect(isAgentRunTerminal('failed')).toBe(true)
    expect(isAgentRunTerminal('canceled')).toBe(true)
  })

  it('uses the DTO cancelable field instead of deriving from status', () => {
    expect(canCancelAgentRun(queuedRun)).toBe(true)
    expect(canCancelAgentRun({ ...queuedRun, cancelable: false })).toBe(false)
  })

  it('provides frozen local labels without exposing raw tool data', () => {
    expect(agentRunStatusLabels.processing).toBe('执行中')
    expect(agentStepStatusLabels.pending).toBe('等待中')
    expect(agentToolLabels.search_documents).toBe('检索文档')
    expect(agentToolLabels.read_document_evidence).toBe('读取证据')
  })
})
