// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { requestJSON } from '@/lib/api-client'
import { reportClientError } from './api'
import { recordRuntimeLog } from '@/lib/runtimeLog'

vi.mock('@/lib/api-client', () => ({ requestJSON: vi.fn() }))

describe('runtime telemetry content boundary', () => {
  afterEach(() => vi.restoreAllMocks())
  beforeEach(() => {
    vi.clearAllMocks()
    vi.mocked(requestJSON).mockResolvedValue({})
    vi.spyOn(console, 'error').mockImplementation(() => {})
    localStorage.clear()
  })

  it('keeps private exception diagnostics in local logs and reports only a fixed kind', async () => {
    recordRuntimeLog({ type: 'react_error', message: 'private manuscript', reason: 'private filename', stack: 'secret-key /private/project' })
    await Promise.resolve()
    expect(requestJSON).toHaveBeenCalledExactlyOnceWith('/api/analytics/error', {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: '{"error_code":"react_error"}', suppressBackendUnavailableToast: true,
    })
    const payload = JSON.stringify(vi.mocked(requestJSON).mock.calls)
    expect(payload).not.toContain('private')
    expect(payload).not.toContain('secret-key')
    expect(localStorage.getItem('nova.runtime.logs')).toContain('private manuscript')
  })

  it('leaves an exporter failure as an optional rejected request without another telemetry event', async () => {
    vi.mocked(requestJSON).mockRejectedValueOnce(new Error('offline'))
    await expect(reportClientError('react_error')).rejects.toThrow('offline')
    expect(requestJSON).toHaveBeenCalledTimes(1)
  })
})
