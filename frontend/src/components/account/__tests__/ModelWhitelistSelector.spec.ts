import { beforeEach, describe, expect, it, vi } from 'vitest'
import { flushPromises, mount } from '@vue/test-utils'

const copyToClipboard = vi.fn().mockResolvedValue(true)

vi.mock('vue-i18n', async () => {
  const actual = await vi.importActual<typeof import('vue-i18n')>('vue-i18n')
  return {
    ...actual,
    useI18n: () => ({
      t: (key: string) => (key === 'common.copy' ? '复制' : key)
    })
  }
})

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
    showInfo: vi.fn()
  })
}))

vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({
    copyToClipboard
  })
}))

import ModelWhitelistSelector from '../ModelWhitelistSelector.vue'

function mountSelector(props: Record<string, unknown> = {}) {
  return mount(ModelWhitelistSelector, {
    props: {
      modelValue: [],
      platform: 'openai',
      ...props
    },
    global: {
      stubs: {
        ModelIcon: true
      }
    }
  })
}

function findModelRow(wrapper: ReturnType<typeof mountSelector>, modelId: string) {
  const row = wrapper
    .findAll('[data-testid="model-option"]')
    .find(candidate => candidate.text().includes(modelId))

  if (!row) {
    throw new Error(`Model row not found: ${modelId}`)
  }

  return row
}

describe('ModelWhitelistSelector', () => {
  beforeEach(() => {
    copyToClipboard.mockClear()
  })

  it('can hide sync actions while retaining clear all', () => {
    const wrapper = mountSelector({
      modelValue: ['claude-sonnet-4-6'],
      platform: 'anthropic',
      accountId: 1,
      showSyncActions: false
    })

    expect(wrapper.text()).not.toContain('admin.accounts.fillRelatedModels')
    expect(wrapper.text()).not.toContain('admin.accounts.syncUpstreamModels')
    expect(wrapper.text()).toContain('admin.accounts.clearAllModels')
  })

  it('clear all emits an empty model list', async () => {
    const wrapper = mountSelector({
      modelValue: ['claude-sonnet-4-6'],
      showSyncActions: false
    })

    const clearButton = wrapper
      .findAll('button')
      .find(button => button.text().includes('admin.accounts.clearAllModels'))
    expect(clearButton).toBeTruthy()

    await clearButton!.trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[[]]])
  })

  it('includes platform-specific models when combining candidates', async () => {
    const wrapper = mountSelector({
      platforms: ['antigravity', 'openai'],
      showSyncActions: false
    })

    await wrapper.get('div.cursor-pointer').trigger('click')

    expect(wrapper.text()).toContain('gpt-oss-120b-medium')
    expect(wrapper.text()).toContain('gpt-5.4')
  })

  it('shows sync actions by default for account forms', () => {
    const wrapper = mountSelector({
      platform: 'anthropic'
    })

    expect(wrapper.text()).toContain('admin.accounts.fillRelatedModels')
    expect(wrapper.text()).toContain('admin.accounts.clearAllModels')
  })

  it('uses explicit available models instead of platform defaults', async () => {
    const wrapper = mountSelector({
      platform: 'openai',
      availableModels: ['group-only-model'],
      showSyncActions: false
    })

    await wrapper.get('div.cursor-pointer').trigger('click')

    expect(wrapper.text()).toContain('group-only-model')
    expect(wrapper.text()).not.toContain('gpt-5.4')
  })

  it('copies a model ID without selecting the model', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')

    const copyButton = row.get('[data-testid="copy-model-id"]')
    expect(copyButton.attributes('aria-label')).toBe('复制 gpt-5.6-sol')

    await copyButton.trigger('click')
    await flushPromises()

    expect(copyToClipboard).toHaveBeenCalledWith('gpt-5.6-sol')
    expect(wrapper.emitted('update:modelValue')).toBeUndefined()
  })

  it('keeps the existing model selection behavior', async () => {
    const wrapper = mountSelector()
    await wrapper.get('div.cursor-pointer').trigger('click')

    const row = findModelRow(wrapper, 'gpt-5.6-sol')
    await row.get('[data-testid="select-model"]').trigger('click')

    expect(wrapper.emitted('update:modelValue')).toEqual([[['gpt-5.6-sol']]])
    expect(copyToClipboard).not.toHaveBeenCalled()
  })
})
