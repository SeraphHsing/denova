import { jsonHeaders, requestJSON } from '@/lib/api-client'
import type { AgentEngineID, EngineDescriptor, EngineModels } from './types'

export const fetchAgentEngines = () => requestJSON<{ items: EngineDescriptor[] }>('/api/agent-runtimes')
export const fetchEngineModels = (id: AgentEngineID) => requestJSON<EngineModels>(`/api/agent-runtimes/${id}/models`)
export const AGENT_ENGINE_CHECKED_EVENT = 'denova:agent-engine-checked'

export async function checkAgentEngine(id: AgentEngineID): Promise<EngineDescriptor> {
  try {
    return await requestJSON<EngineDescriptor>(`/api/agent-runtimes/${id}/check`, {
      method: 'POST', headers: jsonHeaders, body: '{}',
    })
  } finally {
    window.dispatchEvent(new Event(AGENT_ENGINE_CHECKED_EVENT))
  }
}
