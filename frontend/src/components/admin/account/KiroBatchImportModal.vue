<template>
  <BaseDialog
    :show="show"
    :title="t('admin.accounts.kiroBatch.title')"
    width="wide"
    close-on-click-outside
    @close="handleClose"
  >
    <form id="kiro-batch-import-form" @submit.prevent="handleImport">
      <div class="grid gap-6 lg:grid-cols-[minmax(0,1.35fr)_minmax(18rem,0.65fr)]">
        <div class="min-w-0">
          <div class="mb-2 flex flex-wrap items-center justify-between gap-2">
            <label for="kiro-batch-keys" class="input-label mb-0">
              {{ t('admin.accounts.kiroBatch.keysLabel') }}
            </label>
            <span class="text-xs text-gray-500 dark:text-dark-400">
              {{ t('admin.accounts.kiroBatch.keyCount', { count: parsed.keys.length }) }}
              <template v-if="parsed.duplicateCount">
                · {{ t('admin.accounts.kiroBatch.duplicateCount', { count: parsed.duplicateCount }) }}
              </template>
            </span>
          </div>
          <textarea
            id="kiro-batch-keys"
            ref="keysInput"
            v-model="keysText"
            class="input min-h-80 resize-y font-mono text-xs leading-6"
            :placeholder="t('admin.accounts.kiroBatch.keysPlaceholder')"
            autocomplete="off"
            autocapitalize="off"
            spellcheck="false"
            :disabled="importing"
            @input="clearResult"
          ></textarea>
        </div>

        <div class="min-w-0 space-y-5 lg:border-l lg:border-gray-200 lg:pl-6 dark:lg:border-dark-700">
          <div>
            <label for="kiro-batch-name-prefix" class="input-label">
              {{ t('admin.accounts.kiroBatch.namePrefix') }}
            </label>
            <input
              id="kiro-batch-name-prefix"
              v-model="namePrefix"
              type="text"
              class="input"
              autocomplete="off"
              :disabled="importing"
              @input="clearResult"
            />
            <p class="input-hint">
              {{ t('admin.accounts.kiroBatch.namePreview', { name: firstAccountName }) }}
            </p>
          </div>

          <GroupSelector
            v-model="selectedGroupIds"
            :groups="groups"
            platform="kiro"
            :label="t('admin.accounts.kiroBatch.groupsLabel')"
          />

          <dl class="divide-y divide-gray-100 border-y border-gray-200 text-sm dark:divide-dark-700 dark:border-dark-700">
            <div class="flex items-center justify-between gap-4 py-2.5">
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.kiroBatch.readyCount') }}</dt>
              <dd class="font-medium text-gray-900 dark:text-white">{{ parsed.keys.length }}</dd>
            </div>
            <div class="flex items-center justify-between gap-4 py-2.5">
              <dt class="text-gray-500 dark:text-dark-400">{{ t('admin.accounts.kiroBatch.selectedGroups') }}</dt>
              <dd class="font-medium text-gray-900 dark:text-white">{{ selectedGroupIds.length }}</dd>
            </div>
          </dl>
        </div>
      </div>

      <div v-if="result" class="mt-6 border-t border-gray-200 pt-4 dark:border-dark-700">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h4 class="text-sm font-medium text-gray-900 dark:text-white">
            {{ t('admin.accounts.kiroBatch.resultTitle') }}
          </h4>
          <span class="text-sm text-gray-600 dark:text-dark-300">
            {{ t('admin.accounts.kiroBatch.resultSummary', result) }}
          </span>
        </div>
        <div
          v-if="failedItems.length"
          class="mt-3 max-h-40 overflow-auto rounded-lg bg-red-50 p-3 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300"
        >
          <div v-for="item in failedItems" :key="item.name" class="py-0.5">
            <span class="font-medium">{{ item.name }}</span>: {{ item.error }}
          </div>
        </div>
      </div>
    </form>

    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-end gap-3">
        <button type="button" class="btn btn-secondary" :disabled="importing" @click="handleClose">
          {{ t('common.cancel') }}
        </button>
        <button
          type="button"
          class="btn btn-secondary"
          :disabled="!canSubmit || importing"
          @click="downloadJson"
        >
          <Icon name="download" size="sm" class="mr-1.5" />
          {{ t('admin.accounts.kiroBatch.downloadJson') }}
        </button>
        <button
          type="submit"
          form="kiro-batch-import-form"
          class="btn btn-primary"
          :disabled="!canSubmit || importing"
        >
          <Icon v-if="!importing" name="upload" size="sm" class="mr-1.5" />
          {{ importing ? t('admin.accounts.kiroBatch.importing') : t('admin.accounts.kiroBatch.importNow') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import GroupSelector from '@/components/common/GroupSelector.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import { useAppStore } from '@/stores/app'
import type { AdminGroup } from '@/types'
import {
  buildKiroBatchAccounts,
  buildKiroImportPayload,
  createKiroBatchNamePrefix,
  parseKiroKeys
} from '@/utils/kiroBatchImport'

interface Props {
  show: boolean
  groups: AdminGroup[]
}

interface Emits {
  (e: 'close'): void
  (e: 'imported'): void
}

interface BatchResult {
  success: number
  failed: number
  results: Array<{ success: boolean; error?: string }>
}

const props = defineProps<Props>()
const emit = defineEmits<Emits>()
const { t } = useI18n()
const appStore = useAppStore()

const keysInput = ref<HTMLTextAreaElement | null>(null)
const keysText = ref('')
const namePrefix = ref('')
const selectedGroupIds = ref<number[]>([])
const importing = ref(false)
const hasCreatedData = ref(false)
const result = ref<BatchResult | null>(null)

const parsed = computed(() => parseKiroKeys(keysText.value))
const generatedAccounts = computed(() => buildKiroBatchAccounts(parsed.value.keys, {
  namePrefix: namePrefix.value,
  groupIds: selectedGroupIds.value
}))
const firstAccountName = computed(() => generatedAccounts.value[0]?.name || `${namePrefix.value.trim() || 'kiro'}-001`)
const canSubmit = computed(() => (
  parsed.value.keys.length > 0 &&
  namePrefix.value.trim().length > 0 &&
  selectedGroupIds.value.length > 0
))
const failedItems = computed(() => {
  if (!result.value) return []
  return result.value.results.flatMap((item, index) => item.success ? [] : [{
    name: generatedAccounts.value[index]?.name || `#${index + 1}`,
    error: item.error || t('admin.accounts.kiroBatch.unknownError')
  }])
})

watch(
  () => props.show,
  async (open) => {
    if (!open) return
    keysText.value = ''
    namePrefix.value = createKiroBatchNamePrefix()
    selectedGroupIds.value = []
    importing.value = false
    hasCreatedData.value = false
    result.value = null
    await nextTick()
    keysInput.value?.focus()
  },
  { immediate: true }
)

watch(selectedGroupIds, () => {
  result.value = null
}, { deep: true })

const clearResult = () => {
  result.value = null
}

const validate = (): boolean => {
  if (!parsed.value.keys.length) {
    appStore.showError(t('admin.accounts.kiroBatch.keysRequired'))
    return false
  }
  if (!namePrefix.value.trim()) {
    appStore.showError(t('admin.accounts.kiroBatch.namePrefixRequired'))
    return false
  }
  if (!selectedGroupIds.value.length) {
    appStore.showError(t('admin.accounts.kiroBatch.groupsRequired'))
    return false
  }
  return true
}

const handleClose = () => {
  if (importing.value) return
  if (hasCreatedData.value) emit('imported')
  emit('close')
}

const downloadJson = () => {
  if (!validate()) return
  const payload = buildKiroImportPayload(generatedAccounts.value)
  const blob = new Blob([JSON.stringify(payload, null, 2)], { type: 'application/json' })
  const url = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = url
  link.download = `${namePrefix.value.trim()}.json`
  link.click()
  URL.revokeObjectURL(url)
  appStore.showSuccess(t('admin.accounts.kiroBatch.downloaded', { count: parsed.value.keys.length }))
}

const handleImport = async () => {
  if (!validate()) return
  importing.value = true
  result.value = null
  try {
    const response = await adminAPI.accounts.batchCreate(generatedAccounts.value)
    result.value = response
    hasCreatedData.value = response.success > 0
    if (response.failed > 0) {
      appStore.showWarning(t('admin.accounts.kiroBatch.completedWithErrors', response))
      return
    }
    appStore.showSuccess(t('admin.accounts.kiroBatch.imported', { count: response.success }))
    emit('imported')
  } catch (error: any) {
    appStore.showError(error?.message || t('admin.accounts.kiroBatch.importFailed'))
  } finally {
    importing.value = false
  }
}
</script>
