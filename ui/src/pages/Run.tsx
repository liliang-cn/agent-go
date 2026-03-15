import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSquads, useAgents, useDispatchAgentTask, useDispatchSquadTask } from '../hooks/useApi'
import type { AgentModel, Squad } from '../lib/api'

// Agent Chat Component
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
  const [isLoading, setIsLoading] = useState(false)
  const dispatchAgent = useDispatchAgentTask()

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

    setIsLoading(true)
    setResponse('')
    try {
      const result = await dispatchAgent.mutateAsync({
        name: selectedAgent.name,
        instruction: input.trim(),
      })
      setResponse(result.response)
    } catch (err) {
      setResponse(err instanceof Error ? err.message : 'Error')
    } finally {
      setIsLoading(false)
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
          disabled={isLoading}
        />
        <button
          onClick={handleRun}
          disabled={isLoading || !input.trim() || !selectedAgent}
          className="dashboard-button px-6"
        >
          {isLoading ? t('running') : t('run')}
        </button>
      </div>

      {response && (
        <div className="glass-panel rounded-xl border border-slate-100 p-4 text-sm text-slate-700 whitespace-pre-wrap">
          {response}
        </div>
      )}
    </div>
  )
}

// Squad Chat Component
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
  const [ackMessage, setAckMessage] = useState('')
  const [isLoading, setIsLoading] = useState(false)
  const dispatchSquad = useDispatchSquadTask()

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
    setAckMessage('')
    try {
      const result = await dispatchSquad.mutateAsync({
        squadId: selectedSquad.id,
        message: message.trim(),
      })
      setAckMessage(result.ack_message)
    } catch (err) {
      setAckMessage(err instanceof Error ? err.message : 'Error')
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
            setAckMessage('')
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
          onClick={handleRun}
          disabled={isLoading || !message.trim() || !selectedSquad}
          className="dashboard-button px-6"
        >
          {isLoading ? t('running') : t('run')}
        </button>
      </div>

      {ackMessage && (
        <div className="glass-panel rounded-xl border border-slate-100 p-4 text-sm text-slate-700 whitespace-pre-wrap">
          {ackMessage}
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

  // Separate built-in and custom agents
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

      {/* Section 1: Squad */}
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

      {/* Section 2: Agent */}
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
