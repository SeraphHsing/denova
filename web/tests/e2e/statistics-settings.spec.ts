import { expect, test } from '../support/fixtures'
import { createAndOpenBook, createProjectFile, saveProjectFile, createStartedStory } from '../support/api'
import { getModelStatus, releaseDelayedRequest } from '../support/model'
import type { StatisticsReport } from '../../src/features/statistics/api'

test('local statistics can pause capture and resume without counting the paused manuscript interval', async ({ page, request, baseURL }) => {
  const book = await createAndOpenBook(request, 'Optional local statistics')
  const file = 'chapters/ch00001-Test.md'
  await createProjectFile(request, book.projectId, file, '文'.repeat(100))
  const read = async () => {
    const response = await request.get(`/api/statistics?days=1&timezone=UTC&project_id=${book.projectId}`)
    expect(response.ok(), await response.text()).toBe(true)
    return response.json() as Promise<StatisticsReport>
  }
  const generate = async (message: string) => {
    const session = await request.post('/api/sessions', { data: { title: 'Statistics capture' } })
    expect(session.ok()).toBe(true)
    const { id } = await session.json()
    const response = await request.post('/api/chat', { data: { session_id: id, command_id: `statistics-${id}`, message } })
    expect(response.ok(), await response.text()).toBe(true)
  }
  // Keep file invalidations subscribed while the settings route is visible.
  const controller = new AbortController()
  const stream = await fetch(new URL(`/api/projects/${book.projectId}/events`, baseURL), {
    headers: { Accept: 'text/event-stream', 'Accept-Encoding': 'identity' }, signal: controller.signal,
  })
  const reader = stream.body!.getReader()
  await reader.read()
  const marker = 'E2E_DELAYED_AGENT_REPLY'
  let delayed: Promise<void> | undefined
  try {
    await expect.poll(async () => (await read()).writing.projects[0]?.words).toBe(100)
    await generate('Reply with a short acknowledgement.')
    const before = await read()
    expect(before.usage.totals.requests).toBeGreaterThan(0)
    await page.goto('/')
    const sidebar = page.getByLabel('工作台侧边栏')
    await sidebar.getByRole('button', { name: '统计', exact: true }).click()
    await expect(page.getByTestId('statistics-page')).toBeVisible()
    delayed = generate(marker)
    await expect.poll(async () => (await getModelStatus(request)).delayed_waiting_by_marker[marker] ?? 0).toBe(1)
    await sidebar.getByRole('button', { name: '设置', exact: true }).click()
    const toggle = page.getByRole('switch', { name: '启用本地统计', exact: true })
    await expect(toggle).toBeChecked()
    await toggle.click()
    await expect.poll(async () => (await (await request.get('/api/settings')).json()).effective.statistics_enabled).toBe(false)
    await expect(sidebar.getByRole('button', { name: '统计', exact: true })).toHaveCount(0)
    const disabled = await request.get('/api/statistics')
    expect(disabled.status()).toBe(403)
    expect((await disabled.json()).error).toBe('本地统计已关闭')
    await saveProjectFile(request, book.projectId, file, '文'.repeat(250))
    const story = await createStartedStory(request, 'Activity while statistics are disabled')
    // Closing settings returns to the retained route, with an explicit disabled state.
    await sidebar.getByRole('button', { name: '设置', exact: true }).click()
    await expect(page.getByText('本地统计已关闭', { exact: true })).toBeVisible()
    await page.getByRole('button', { name: '前往设置', exact: true }).click()
    await expect(toggle).not.toBeChecked()
    await toggle.scrollIntoViewIfNeeded()
    await page.screenshot({ animations: 'disabled', path: test.info().outputPath('statistics-setting-dark.png') })
    await page.reload()
    await expect(toggle).not.toBeChecked()
    for (const origin of ['写作', '游戏']) {
      await sidebar.getByRole('button', { name: origin, exact: true }).click()
      await expect(sidebar.getByRole('button', { name: '统计', exact: true })).toHaveCount(0)
      await sidebar.getByRole('button', { name: '设置', exact: true }).click()
      await expect(toggle).not.toBeChecked()
    }
    await toggle.click()
    await expect.poll(async () => (await (await request.get('/api/settings')).json()).effective.statistics_enabled).toBe(true)
    await expect(sidebar.getByRole('button', { name: '统计', exact: true })).toBeVisible()
    // A call spanning disable/re-enable must not leak its paused usage on completion.
    await releaseDelayedRequest(request, marker)
    await delayed
    const resumed = await read()
    expect(resumed.usage.totals).toEqual(before.usage.totals)
    expect(resumed.writing.projects[0].words).toBe(250)
    expect(resumed.writing.days[0].delta).toBe(before.writing.days[0].delta)
    expect(resumed.game.stories.find(item => item.id === story.id)?.turns).toBe(1)
    await saveProjectFile(request, book.projectId, file, '文'.repeat(270))
    await expect.poll(async () => (await read()).writing.days[0].delta).toBe((before.writing.days[0].delta ?? 0) + 20)
    await generate('Reply with a short acknowledgement.')
    await expect.poll(async () => (await read()).usage.totals.requests).toBeGreaterThan(before.usage.totals.requests)
    for (const origin of ['写作', '游戏']) {
      await sidebar.getByRole('button', { name: origin, exact: true }).click()
      await sidebar.getByRole('button', { name: '统计', exact: true }).click()
      await expect(page.getByTestId('statistics-page')).toBeVisible()
    }
    const settings = await (await request.get('/api/settings')).json()
    const updated = await request.patch('/api/settings', { data: {
      layer: 'user', base_revision: settings.revisions.user, changes: { theme: 'light', language: 'en-US' },
    } })
    expect(updated.ok()).toBe(true)
    await page.setViewportSize({ width: 390, height: 844 })
    await page.reload()
    await page.getByRole('button', { name: 'Navigation', exact: true }).click()
    await page.getByRole('button', { name: 'Settings', exact: true }).click()
    const englishToggle = page.getByRole('switch', { name: 'Enable local statistics', exact: true })
    await expect(englishToggle).toBeChecked()
    await englishToggle.scrollIntoViewIfNeeded()
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light')
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
    await page.screenshot({ animations: 'disabled', path: test.info().outputPath('statistics-setting-light-narrow.png') })
  } finally {
    await releaseDelayedRequest(request, marker)
    await delayed
    controller.abort()
    await reader.cancel().catch(() => {})
  }
})
