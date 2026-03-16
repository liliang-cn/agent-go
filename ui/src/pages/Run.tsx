import { useEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useAgents, useDispatchSquadTask, useSquads } from '../hooks/useApi'
import type { AgentModel, DispatchSquadTaskResponse, Squad, SquadTask } from '../lib/api'

type AgentRunStatus = 'idle' | 'streaming'

function sleep(ms: number) {
  return new Promise((resolve) => window.setTimeout(resolve, ms))
}

async function streamAgentDispatch(
  agentName: string,
  instruction: string,
  onEvent: (event: Record<string, unknown>) => void,
) {
  const response = await fetch(`/api/agents/${encodeURIComponent(agentName)}/dispatch/stream`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ instruction }),
  })

  if (!response.ok) {
    const error = await response.json().catch(() => ({ error: `HTTP ${response.status}` }))
    throw new Error(error.error || `HTTP ${response.status}`)
  }

  if (!response.body) {
    return
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) {
      break
    }

    buffer += decoder.decode(value, { stream: true })
    const chunks = buffer.split('\n\n')
    buffer = chunks.pop() || ''

    for (const chunk of chunks) {
      const dataLines = chunk
        .split('\n')
        .filter((line) => line.startsWith('data: '))
        .map((line) => line.slice(6))

      for (const data of dataLines) {
        if (data === '[DONE]') {
          return
        }
        const parsed = JSON.parse(data) as Record<string, unknown>
        onEvent(parsed)
      }
    }
  }
}

async function pollSquadTask(squadId: string, taskId: string): Promise<SquadTask> {
  for (let attempt = 0; attempt < 90; attempt++) {
    const response = await fetch(`/api/squads/tasks?squad_id=${encodeURIComponent(squadId)}&limit=50`)
    if (!response.ok) {
      throw new Error(`HTTP ${response.status}`)
    }

    const payload = (await response.json()) as { tasks?: SquadTask[] }
    const task = (payload.tasks ?? []).find((item) => item.id === taskId)
    if (task && (task.status === 'completed' || task.status === 'failed')) {
      return task
    }

    await sleep(1200)
  }

  throw new Error('Timed out waiting for squad task result')
}

function AgentChat({
  agents,
  onSelect,
}: {
  agents: AgentModel[]
  onSelect: (agent: AgentModel | null) => void
}) {
  const { t } = useTranslation()
  const [selectedAgent, setSelectedAgent] = useState<AgentModel | null>(null)
  const [input, setInput] = useState('')
  const [response, setResponse] = useState('')
  const [status, setStatus] = useState<AgentRunStatus>('idle')
  const streamRunId = useRef(0)

  useEffect(() => {
    if (agents.length > 0 && !selectedAgent) {
      setSelectedAgent(agents[0])
    }
  }, [agents, selectedAgent])

  useEffect(() => {
    onSelect(selectedAgent)
  }, [selectedAgent, onSelect])

  const handleRun = async () => {
    if (!selectedAgent || !input.trim()) return

    const runId = Date.now()
    streamRunId.current = runId
    setStatus('streaming')
    setResponse('')

    try {
      let aggregated = ''
      await streamAgentDispatch(selectedAgent.name, input.trim(), (event) => {
        if (streamRunId.current !== runId) {
          return
        }

        const type = String(event.type || '')
        const content = typeof event.content === 'string' ? event.content : ''

        if (type === 'partial') {
          aggregated += content
          setResponse(aggregated)
          return
        }

        if (type === 'workflow_complete') {
          setResponse(content || aggregated)
          return
        }

        if (type === 'workflow_error') {
          setResponse(content || 'Error')
        }
      })
    } catch (err) {
      if (streamRunId.current === runId) {
        setResponse(err instanceof Error ? err.message : 'Error')
      }
    } finally {
      if (streamRunId.current === runId) {
        setStatus('idle')
      }
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-3">
        <select
          value={selectedAgent?.id || ''}
          onChange={(e) => {
            const agent = agents.find((a) => a.id === e.target.value)
            setSelectedAgent(agent || null)
            setResponse('')
          }}
          className="flex-1 rounded-xl border border-sky-100 bg-white px-4 py-2.5 text-sm text-slate-700 shadow-sm focus:border-sky-300 focus:outline-none focus:ring-2 focus:ring-sky-100"
        >
          {agents.map((agent) => (
            <option key={agent.id} value={agent.id}>
              {agent.name} ({agent.kind})
            </option>
          ))}
        </select>
      </div>

      {selectedAgent && (
        <div className="rounded-xl border border-slate-100 bg-slate-50/50 px-4 py-3 text-sm text-slate-600">
          {selectedAgent.description}
        </div>
      )}

      <div className="flex gap-3">
        <input
          type="text"
          value={input}
          onChange={(e) => setInput(e.target.value)}
          placeholder={t('instructionPlaceholder')}
          className="dashboard-input flex-1"
          disabled={status === 'streaming'}
        />
        <button
          type="button"
          onClick={handleRun}
          disabled={status === 'streaming' || !input.trim() || !selectedAgent}
          className="dashboard-button px-6"
        >
          {status === 'streaming' ? t('running') : t('run')}
        </button>
      </div>

      {status === 'streaming' && !response && (
        <div className="rounded-xl border border-sky-100 bg-sky-50/50 px-4 py-3 text-sm text-sky-700">
          {t('runAgentStreaming')}
        </div>
      )}

      {response && (
        <div className="glass-panel rounded-xl border border-slate-100 p-4 text-sm text-slate-700 whitespace-pre-wrap">
          {response}
        </div>
      )}
    </div>
  )
}

function SquadChat({
  squads,
  onSelect,
}: {
  squads: Squad[]
  onSelect: (squad: Squad | null) => void
}) {
  const { t } = useTranslation()
  const [selectedSquad, setSelectedSquad] = useState<Squad | null>(null)
  const [message, setMessage] = useState('')
  const [output, setOutput] = useState('')
  const [isLoading, setIsLoading] = useState(false)
  const dispatchSquad = useDispatchSquadTask()
  const activeTaskIdRef = useRef<string | null>(null)

  useEffect(() => {
    if (squads.length > 0 && !selectedSquad) {
      setSelectedSquad(squads[0])
    }
  }, [squads, selectedSquad])

  useEffect(() => {
    onSelect(selectedSquad)
  }, [selectedSquad, onSelect])

  const handleRun = async () => {
    if (!selectedSquad || !message.trim()) return

    setIsLoading(true)
    setOutput('')
    try {
      const result = await dispatchSquad.mutateAsync({
        squadId: selectedSquad.id,
        message: message.trim(),
      })

      const payload = result as DispatchSquadTaskResponse & { task?: SquadTask }
      setOutput(payload.ack_message || t('runSquadQueued'))

      if (payload.task?.id) {
        activeTaskIdRef.current = payload.task.id
        const completedTask = await pollSquadTask(selectedSquad.id, payload.task.id)
        if (activeTaskIdRef.current === payload.task.id) {
          setOutput(completedTask.result_text || payload.ack_message || t('runSquadQueued'))
        }
      }
    } catch (err) {
      setOutput(err instanceof Error ? err.message : 'Error')
    } finally {
      setIsLoading(false)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex items-center gap-3">
        <select
          value={selectedSquad?.id || ''}
          onChange={(e) => {
            const squad = squads.find((s) => s.id === e.target.value)
            setSelectedSquad(squad || null)
            setOutput('')
          }}
          className="flex-1 rounded-xl border border-sky-100 bg-white px-4 py-2.5 text-sm text-slate-700 shadow-sm focus:border-sky-300 focus:outline-none focus:ring-2 focus:ring-sky-100"
        >
          {squads.map((squad) => (
            <option key={squad.id} value={squad.id}>
              {squad.name} ({squad.members.length} {t('members')})
            </option>
          ))}
        </select>
      </div>

      {selectedSquad && (
        <div className="flex flex-wrap gap-x-4 gap-y-1 text-sm text-slate-500">
          <span>
            <span className="font-medium">{t('captainLabel')}:</span>{' '}
            {selectedSquad.lead_agent?.name ?? selectedSquad.captain?.name ?? '-'}
          </span>
          <span>
            <span className="font-medium">{t('members')}:</span> {selectedSquad.members.length}
          </span>
        </div>
      )}

      <div className="flex gap-3">
        <input
          type="text"
          value={message}
          onChange={(e) => setMessage(e.target.value)}
          placeholder={t('messagePlaceholder')}
          className="dashboard-input flex-1"
          disabled={isLoading}
        />
        <button
          type="button"
          onClick={handleRun}
          disabled={isLoading || !message.trim() || !selectedSquad}
          className="dashboard-button px-6"
        >
          {isLoading ? t('running') : t('run')}
        </button>
      </div>

      {isLoading && !output && (
        <div className="rounded-xl border border-emerald-100 bg-emerald-50/50 px-4 py-3 text-sm text-emerald-700">
          {t('runSquadQueued')}
        </div>
      )}

      {output && (
        <div className="glass-panel rounded-xl border border-slate-100 p-4 text-sm text-slate-700 whitespace-pre-wrap">
          {output}
        </div>
      )}
    </div>
  )
}

export function Run() {
  const { t } = useTranslation()
  const { data: squads = [] } = useSquads()
  const { data: agents = [] } = useAgents()
  const [debugEnabled, setDebugEnabled] = useState(false)

  const { builtinAgents, customAgents } = useMemo(() => {
    const builtin: AgentModel[] = []
    const custom: AgentModel[] = []
    const builtinNames = ['Concierge', 'Assistant', 'Operator', 'Captain', 'Stakeholder']

    agents.forEach((agent) => {
      if (builtinNames.some((name) => agent.name.toLowerCase() === name.toLowerCase())) {
        builtin.push(agent)
      } else {
        custom.push(agent)
      }
    })

    return { builtinAgents: builtin, customAgents: custom }
  }, [agents])

  const allAgents = useMemo(() => [...builtinAgents, ...customAgents], [builtinAgents, customAgents])

  const [selectedSquad, setSelectedSquad] = useState<Squad | null>(null)
  const [selectedAgent, setSelectedAgent] = useState<AgentModel | null>(null)

  return (
    <div className="space-y-8" data-testid="page-run">
      <div className="flex items-start justify-between">
        <div>
          <h2 className="text-2xl font-semibold text-slate-900">{t('run')}</h2>
          <p className="mt-1 text-sm text-slate-500">{t('runPageDescription')}</p>
        </div>
        <label className="inline-flex cursor-pointer items-center gap-3 rounded-2xl border border-slate-200 bg-white px-4 py-2.5 text-sm font-medium text-slate-700 shadow-sm transition-colors hover:bg-slate-50">
          <span>{t('debug')}</span>
          <button
            type="button"
            role="switch"
            aria-checked={debugEnabled}
            onClick={() => setDebugEnabled(!debugEnabled)}
            className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${debugEnabled ? 'bg-blue-600' : 'bg-slate-200'}`}
          >
            <span
              className={`inline-block h-5 w-5 transform rounded-full bg-white shadow-sm transition-transform ${debugEnabled ? 'translate-x-5' : 'translate-x-1'}`}
            />
          </button>
        </label>
      </div>

      {debugEnabled && (
        <div className="glass-panel rounded-[24px] border border-slate-200 p-5">
          <div className="text-sm font-semibold text-slate-900">{t('debugInfo')}</div>
          <div className="mt-3 grid grid-cols-2 gap-4 text-sm text-slate-600">
            <div className="rounded-xl bg-slate-50 px-4 py-3">
              <div className="text-xs font-medium uppercase tracking-wide text-slate-400">{t('squadsCount')}</div>
              <div className="mt-1 text-xl font-semibold text-slate-900">{squads.length}</div>
            </div>
            <div className="rounded-xl bg-slate-50 px-4 py-3">
              <div className="text-xs font-medium uppercase tracking-wide text-slate-400">{t('agentsCount')}</div>
              <div className="mt-1 text-xl font-semibold text-slate-900">{agents.length}</div>
            </div>
            <div className="rounded-xl bg-slate-50 px-4 py-3">
              <div className="text-xs font-medium uppercase tracking-wide text-slate-400">{t('builtinAgentsCount')}</div>
              <div className="mt-1 text-xl font-semibold text-emerald-600">{builtinAgents.length}</div>
            </div>
            <div className="rounded-xl bg-slate-50 px-4 py-3">
              <div className="text-xs font-medium uppercase tracking-wide text-slate-400">{t('customAgentsCount')}</div>
              <div className="mt-1 text-xl font-semibold text-sky-600">{customAgents.length}</div>
            </div>
          </div>
        </div>
      )}

      <section className="glass-panel rounded-[24px] border border-emerald-100/50 p-6">
        <div className="mb-4 flex items-center gap-3">
          <span className="rounded-xl bg-gradient-to-br from-emerald-400 to-emerald-500 px-3 py-1.5 text-xs font-semibold text-white shadow-md shadow-emerald-500/20">
            {t('squads')}
          </span>
          <h3 className="text-lg font-semibold text-slate-900">{t('runSquad')}</h3>
        </div>
        {squads.length === 0 ? (
          <p className="rounded-xl bg-slate-50 px-4 py-8 text-center text-sm text-slate-500">{t('noSquadsRegistered')}</p>
        ) : (
          <SquadChat squads={squads} onSelect={setSelectedSquad} />
        )}
      </section>

      <section className="glass-panel rounded-[24px] border border-sky-100/50 p-6">
        <div className="mb-4 flex items-center gap-3">
          <span className="rounded-xl bg-gradient-to-br from-sky-400 to-sky-500 px-3 py-1.5 text-xs font-semibold text-white shadow-md shadow-sky-500/20">
            {t('agent')}
          </span>
          <h3 className="text-lg font-semibold text-slate-900">{t('runAgent')}</h3>
        </div>
        {allAgents.length === 0 ? (
          <p className="rounded-xl bg-slate-50 px-4 py-8 text-center text-sm text-slate-500">{t('noAgentsRegistered')}</p>
        ) : (
          <AgentChat agents={allAgents} onSelect={setSelectedAgent} />
        )}
      </section>
    </div>
  )
}
