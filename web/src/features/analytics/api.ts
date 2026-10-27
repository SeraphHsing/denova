import { requestJSON } from '@/lib/api-client'

export interface AnalyticsPreferences {
  enabled: boolean
  notice_seen: boolean
  available: boolean
}
export type AnalyticsFeature = 'writing' | 'game' | 'chat' | 'image' | 'library' | 'versions' | 'automation' | 'projects' | 'resources'
export const ANALYTICS_PREFERENCES_CHANGED = 'denova:analytics-preferences-changed'
export function fetchAnalyticsPreferences() { return requestJSON<AnalyticsPreferences>('/api/analytics') }
export async function setAnalyticsEnabled(enabled: boolean) {
  const preferences = await requestJSON<AnalyticsPreferences>('/api/analytics', {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ enabled }),
  })
  window.dispatchEvent(new CustomEvent(ANALYTICS_PREFERENCES_CHANGED, { detail: preferences }))
  return preferences
}
// Only a category is sent. Browser URLs, titles, editor content and input
// event details never enter this request.
export function recordActivity(feature: AnalyticsFeature) {
  return requestJSON('/api/analytics/activity', {
    suppressBackendUnavailableToast: true,
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ feature }),
  })
}

export function recordAppOpened() {
  return requestJSON('/api/analytics/open', {
    suppressBackendUnavailableToast: true,
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}',
  })
}

export type AnalyticsClientError = 'react_error' | 'window_error' | 'unhandled_rejection' | 'white_screen' | 'startup'

// The error object deliberately never enters this boundary. Full diagnostics
// stay in local runtime logs; cloud analytics receives a fixed failure kind.
export function reportClientError(code: AnalyticsClientError) {
  return requestJSON('/api/analytics/error', {
    suppressBackendUnavailableToast: true,
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ error_code: code }),
  })
}
