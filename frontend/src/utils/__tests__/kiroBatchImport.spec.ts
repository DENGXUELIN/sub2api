import { describe, expect, it } from 'vitest'
import {
  buildKiroBatchAccounts,
  buildKiroImportPayload,
  createKiroBatchNamePrefix,
  parseKiroKeys
} from '../kiroBatchImport'

describe('kiroBatchImport', () => {
  it('trims blank lines and removes duplicate keys without reordering', () => {
    expect(parseKiroKeys(' ksk_a \n\nksk_b\r\nksk_a\n')).toEqual({
      keys: ['ksk_a', 'ksk_b'],
      blankCount: 2,
      duplicateCount: 1
    })
  })

  it('builds Kiro API key accounts with selected groups', () => {
    const accounts = buildKiroBatchAccounts(['ksk_a', 'ksk_b'], {
      namePrefix: 'batch',
      groupIds: [7, 9]
    })

    expect(accounts).toEqual([
      expect.objectContaining({
        name: 'batch-001',
        platform: 'kiro',
        type: 'apikey',
        credentials: { api_key: 'ksk_a' },
        concurrency: 1,
        priority: 1,
        group_ids: [7, 9]
      }),
      expect.objectContaining({ name: 'batch-002', credentials: { api_key: 'ksk_b' } })
    ])
  })

  it('creates an uploadable Sub2API payload that retains group ids', () => {
    const accounts = buildKiroBatchAccounts(['ksk_a'], {
      namePrefix: 'kiro-test',
      groupIds: [12]
    })
    const payload = buildKiroImportPayload(accounts, '2026-07-29T00:00:00.000Z')

    expect(payload).toEqual({
      type: 'sub2api-data',
      version: 1,
      exported_at: '2026-07-29T00:00:00.000Z',
      proxies: [],
      accounts: [
        expect.objectContaining({
          name: 'kiro-test-001',
          credentials: { api_key: 'ksk_a' },
          group_ids: [12]
        })
      ]
    })
  })

  it('uses a stable timestamped default prefix', () => {
    expect(createKiroBatchNamePrefix(new Date(2026, 6, 29, 8, 5, 4))).toBe(
      'kiro-20260729-080504'
    )
  })
})
