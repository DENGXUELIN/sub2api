import type { AdminDataPayload, CreateAccountRequest } from '@/types'

export interface ParsedKiroKeys {
  keys: string[]
  blankCount: number
  duplicateCount: number
}

export interface KiroBatchOptions {
  namePrefix: string
  groupIds: number[]
  concurrency?: number
  priority?: number
}

export const parseKiroKeys = (value: string): ParsedKiroKeys => {
  const lines = value.split(/\r?\n/)
  const keys: string[] = []
  const seen = new Set<string>()
  let blankCount = 0
  let duplicateCount = 0

  for (const line of lines) {
    const key = line.trim()
    if (!key) {
      blankCount += 1
      continue
    }
    if (seen.has(key)) {
      duplicateCount += 1
      continue
    }
    seen.add(key)
    keys.push(key)
  }

  return { keys, blankCount, duplicateCount }
}

export const buildKiroBatchAccounts = (
  keys: string[],
  options: KiroBatchOptions
): CreateAccountRequest[] => {
  const prefix = options.namePrefix.trim()
  const width = Math.max(3, String(keys.length).length)

  return keys.map((apiKey, index) => ({
    name: `${prefix}-${String(index + 1).padStart(width, '0')}`,
    platform: 'kiro',
    type: 'apikey',
    credentials: { api_key: apiKey },
    concurrency: options.concurrency ?? 1,
    priority: options.priority ?? 1,
    group_ids: [...options.groupIds]
  }))
}

export const buildKiroImportPayload = (
  accounts: CreateAccountRequest[],
  exportedAt = new Date().toISOString()
): AdminDataPayload => ({
  type: 'sub2api-data',
  version: 1,
  exported_at: exportedAt,
  proxies: [],
  accounts: accounts.map((account) => ({
    name: account.name,
    platform: account.platform,
    type: account.type,
    credentials: account.credentials,
    extra: account.extra,
    concurrency: account.concurrency ?? 1,
    priority: account.priority ?? 1,
    group_ids: account.group_ids
  }))
})

export const createKiroBatchNamePrefix = (date = new Date()): string => {
  const pad = (value: number) => String(value).padStart(2, '0')
  return [
    'kiro',
    `${date.getFullYear()}${pad(date.getMonth() + 1)}${pad(date.getDate())}`,
    `${pad(date.getHours())}${pad(date.getMinutes())}${pad(date.getSeconds())}`
  ].join('-')
}
