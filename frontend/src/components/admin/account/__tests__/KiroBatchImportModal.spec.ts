import { defineComponent } from 'vue'
import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import KiroBatchImportModal from '../KiroBatchImportModal.vue'
import type { AdminGroup } from '@/types'

const { showError, showSuccess, showWarning, batchCreate } = vi.hoisted(() => ({
  showError: vi.fn(),
  showSuccess: vi.fn(),
  showWarning: vi.fn(),
  batchCreate: vi.fn()
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({ showError, showSuccess, showWarning })
}))

vi.mock('@/api/admin', () => ({
  adminAPI: { accounts: { batchCreate } }
}))

vi.mock('vue-i18n', () => ({
  useI18n: () => ({ t: (key: string) => key })
}))

const GroupSelectorStub = defineComponent({
  name: 'GroupSelector',
  props: { modelValue: { type: Array, default: () => [] } },
  emits: ['update:modelValue'],
  template: '<button type="button" data-testid="select-group" @click="$emit(\'update:modelValue\', [7])">group</button>'
})

const groups = [{ id: 7, name: 'kiro', platform: 'kiro' }] as AdminGroup[]

const mountModal = () => mount(KiroBatchImportModal, {
  props: { show: true, groups },
  global: {
    stubs: {
      BaseDialog: { template: '<div><slot /><slot name="footer" /></div>' },
      GroupSelector: GroupSelectorStub,
      Icon: true
    }
  }
})

describe('KiroBatchImportModal', () => {
  beforeEach(() => {
    showError.mockReset()
    showSuccess.mockReset()
    showWarning.mockReset()
    batchCreate.mockReset()
  })

  it('submits unique keys with generated names and selected groups', async () => {
    batchCreate.mockResolvedValue({
      success: 2,
      failed: 0,
      results: [{ success: true }, { success: true }]
    })
    const wrapper = mountModal()

    await wrapper.get('#kiro-batch-keys').setValue('ksk_a\nksk_b\nksk_a')
    await wrapper.get('#kiro-batch-name-prefix').setValue('test-batch')
    await wrapper.get('[data-testid="select-group"]').trigger('click')
    await wrapper.get('form').trigger('submit')
    await flushPromises()

    expect(batchCreate).toHaveBeenCalledWith([
      expect.objectContaining({
        name: 'test-batch-001',
        credentials: { api_key: 'ksk_a' },
        group_ids: [7]
      }),
      expect.objectContaining({
        name: 'test-batch-002',
        credentials: { api_key: 'ksk_b' },
        group_ids: [7]
      })
    ])
    expect(showSuccess).toHaveBeenCalledWith('admin.accounts.kiroBatch.imported')
    expect(wrapper.emitted('imported')).toHaveLength(1)
  })

  it('requires a selected Kiro group', async () => {
    const wrapper = mountModal()
    await wrapper.get('#kiro-batch-keys').setValue('ksk_a')
    await wrapper.get('form').trigger('submit')

    expect(showError).toHaveBeenCalledWith('admin.accounts.kiroBatch.groupsRequired')
    expect(batchCreate).not.toHaveBeenCalled()
  })
})
