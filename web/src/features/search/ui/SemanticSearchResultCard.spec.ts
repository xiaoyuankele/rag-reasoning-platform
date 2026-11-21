import { mount } from '@vue/test-utils'
import { describe, expect, it } from 'vitest'
import type { SemanticSearchHit } from '../../../entities/search-result/model/search-result'
import SemanticSearchResultCard from './SemanticSearchResultCard.vue'

const hit: SemanticSearchHit = {
  chunkId: 11,
  documentId: 7,
  chunkIndex: 2,
  title: null,
  originalName: 'maglev.pdf',
  mimeType: 'application/pdf',
  content: '轨道不平顺会显著影响车体加速度。',
  pageStart: 3,
  pageEnd: 3,
  similarity: 0.9,
}

describe('SemanticSearchResultCard', () => {
  it('优先高亮较长的连续文字命中，避免双字碎片铺满正文', () => {
    const wrapper = mount(SemanticSearchResultCard, {
      props: { hit, query: '轨道不平顺预测车体加速度' },
    })

    expect(wrapper.findAll('mark').map((mark) => mark.text())).toEqual(['轨道不平顺', '车体加速度'])
    expect(wrapper.get('.result-content').text()).toBe(hit.content)
  })

  it('纯语义相关但没有文字重合时保留原文且不伪造高亮', () => {
    const wrapper = mount(SemanticSearchResultCard, {
      props: { hit, query: '悬浮系统稳定性' },
    })

    expect(wrapper.findAll('mark')).toHaveLength(0)
    expect(wrapper.get('.result-content').text()).toBe(hit.content)
  })
})
