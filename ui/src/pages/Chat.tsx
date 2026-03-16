import { useChat } from '@ai-sdk/react'
import { DefaultChatTransport, type UIMessage } from 'ai'
import { useTranslation } from 'react-i18next'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useChatSession, useChatSessions, useMCPTools, useStatus } from '../hooks/useApi'
import type { ChatSessionMessage, MCPTool } from '../lib/api'

type ChatViewMode = 'normal' | 'debug'

function createDraftChatId() {
  return `chat-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`
}

function stringify(value: unknown) {
  if (value == null) return ''
  try {
    return JSON.stringify(value, null, 2)
  } catch {
    return String(value)
  }
}

function normalizeMessageRole(role: string): UIMessage['role'] {
  if (role === 'user' || role === 'assistant' || role === 'system') {
    return role
  }
  return 'assistant'
}

function normalizeSessionMessages(messages: ChatSessionMessage[]): UIMessage[] {
  return messages.map((message, index) => {
    const parts =
      message.parts && message.parts.length > 0
        ? message.parts
        : typeof message.content === 'string'
          ? [{ type: 'text' as const, text: message.content }]
          : []

    return {
      id: message.id || `session-message-${index}`,
      role: normalizeMessageRole(message.role),
      parts,
    }
  })
}

function isToolPart(part: UIMessage['parts'][number]): part is UIMessage['parts'][number] & {
  toolCallId: string
  toolName?: string
  state?: string
  input?: unknown
  output?: unknown
  errorText?: string
} {
  return 'toolCallId' in part
}

function toolPartName(part: UIMessage['parts'][number] & { toolName?: string; type: string }) {
  if (part.toolName) {
    return part.toolName
  }
  if (part.type.startsWith('tool-')) {
    return part.type.slice(5)
  }
  return part.type
}

function renderToolPart(
  part: UIMessage['parts'][number] & {
    toolCallId: string
    toolName?: string
    state?: string
    input?: unknown
    output?: unknown
    errorText?: string
  },
  t: (key: string, options?: Record<string, unknown>) => string,
) {
  const title = toolPartName(part)
  const isFinished = part.state === 'output-available'
  return (
    <details
      key={`${part.toolCallId}-${part.type}`}
      open={!isFinished}
      className="rounded-2xl border border-amber-100 bg-amber-50/70 p-3 text-sm text-slate-700"
    >
      <summary className="flex cursor-pointer list-none items-center justify-between gap-3">
        <span className="font-medium text-slate-900">{title}</span>
        <span className="rounded-full bg-white px-2 py-1 text-[11px] text-slate-500">
          {t('chatToolState')}: {part.state}
        </span>
      </summary>
      <div className="mt-3 space-y-3">
        {part.input !== undefined && (
          <div>
            <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-slate-500">
              {t('chatToolInput')}
            </div>
            <pre className="overflow-x-auto whitespace-pre-wrap rounded-xl bg-white/80 p-3 text-[11px] text-slate-700">
              {stringify(part.input)}
            </pre>
          </div>
        )}
        {part.output !== undefined && (
          <div>
            <div className="mb-1 text-[11px] font-semibold uppercase tracking-[0.18em] text-slate-500">
              {t('chatToolOutput')}
            </div>
            <pre className="overflow-x-auto whitespace-pre-wrap rounded-xl bg-white/80 p-3 text-[11px] text-slate-700">
              {stringify(part.output)}
            </pre>
          </div>
        )}
        {part.errorText && (
          <div className="rounded-xl border border-rose-200 bg-rose-50 p-3 text-[11px] text-rose-700">
            {part.errorText}
          </div>
        )}
      </div>
    </details>
  )
}

function renderMessagePart(
  part: UIMessage['parts'][number],
  index: number,
  viewMode: ChatViewMode,
  t: (key: string, options?: Record<string, unknown>) => string,
) {
  if (part.type === 'text') {
    return (
      <p key={`text-${index}`} className="whitespace-pre-wrap leading-7">
        {part.text}
      </p>
    )
  }

  if (part.type === 'reasoning') {
    if (viewMode !== 'debug') {
      return null
    }
    return (
      <details key={`reasoning-${index}`} className="rounded-2xl border border-sky-100 bg-sky-50/70 p-3 text-sm text-slate-700">
        <summary className="cursor-pointer list-none font-medium text-slate-900">{t('chatReasoning')}</summary>
        <pre className="mt-3 overflow-x-auto whitespace-pre-wrap rounded-xl bg-white/80 p-3 text-[11px] text-slate-700">
          {part.text}
        </pre>
      </details>
    )
  }

  if (isToolPart(part)) {
    return renderToolPart(part, t)
  }

  if (viewMode !== 'debug') {
    return null
  }

  return (
    <pre key={`part-${index}`} className="overflow-x-auto whitespace-pre-wrap rounded-xl bg-slate-950/95 p-3 text-[11px] text-sky-100">
      {stringify(part)}
    </pre>
  )
}

export function Chat() {
  const { t } = useTranslation()
  const { data: status } = useStatus()
  const { data: sessionsData } = useChatSessions(20, 'llm')
  const { data: toolsData = [], isLoading: isToolsLoading } = useMCPTools()
  const [selectedProvider, setSelectedProvider] = useState('')
  const [selectedSessionId, setSelectedSessionId] = useState<string | undefined>(undefined)
  const [draftChatId, setDraftChatId] = useState(createDraftChatId)
  const [input, setInput] = useState('')
  const [selectedToolNames, setSelectedToolNames] = useState<string[]>([])
  const [viewMode, setViewMode] = useState<ChatViewMode>('normal')

  const { data: sessionDetail, isLoading: isSessionLoading } = useChatSession(selectedSessionId)
  const activeChatId = selectedSessionId ?? draftChatId
  const providerRef = useRef(selectedProvider)
  const toolNamesRef = useRef(selectedToolNames)
  const messagesEndRef = useRef<HTMLDivElement>(null)

  const providers = status?.providers?.filter((provider) => provider.status === 'enabled' && provider.type === 'llm') || []

  useEffect(() => {
    if (providers.length > 0 && !selectedProvider) {
      setSelectedProvider(providers[0].name)
    }
  }, [providers, selectedProvider])

  useEffect(() => {
    providerRef.current = selectedProvider
  }, [selectedProvider])

  useEffect(() => {
    toolNamesRef.current = selectedToolNames
  }, [selectedToolNames])

  useEffect(() => {
    const available = new Set(toolsData.map((tool) => tool.name))
    setSelectedToolNames((current) => current.filter((name) => available.has(name)))
  }, [toolsData])

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messagesEndRef, activeChatId])

  const transport = useMemo(
    () =>
      new DefaultChatTransport({
        api: '/api/chat',
        body: () => ({
          mode: 'llm',
          provider: providerRef.current,
          tool_names: toolNamesRef.current,
          max_tool_calls: 6,
        }),
      }),
    [],
  )

  const { sendMessage, status: chatStatus, messages, setMessages } = useChat<UIMessage>({
    id: activeChatId,
    transport,
  })

  useEffect(() => {
    if (!selectedSessionId) {
      return
    }
    if (!sessionDetail || sessionDetail.id !== selectedSessionId) {
      return
    }
    setMessages(normalizeSessionMessages(sessionDetail.messages ?? []))
  }, [selectedSessionId, sessionDetail, setMessages])

  useEffect(() => {
    messagesEndRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const toolMap = useMemo(() => {
    const next = new Map<string, MCPTool>()
    toolsData.forEach((tool) => next.set(tool.name, tool))
    return next
  }, [toolsData])

  const totalParts = useMemo(
    () => messages.reduce((sum, message) => sum + (message.parts?.length ?? 0), 0),
    [messages],
  )
  const lastAssistant = [...messages].reverse().find((message) => message.role === 'assistant')
  const lastAssistantToolParts = lastAssistant?.parts.filter(isToolPart) ?? []

  const handleSelectSession = (sessionId: string) => {
    setSelectedSessionId(sessionId)
    setInput('')
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (!input.trim() || chatStatus !== 'ready' || isSessionLoading) {
      return
    }
    sendMessage({ text: input })
    setInput('')
  }

  const handleNewChat = () => {
    setSelectedSessionId(undefined)
    setDraftChatId(createDraftChatId())
    setInput('')
  }

  const toggleTool = (toolName: string) => {
    setSelectedToolNames((current) =>
      current.includes(toolName)
        ? current.filter((name) => name !== toolName)
        : [...current, toolName],
    )
  }

  const armAllTools = () => {
    setSelectedToolNames(toolsData.map((tool) => tool.name))
  }

  const clearToolSelection = () => {
    setSelectedToolNames([])
  }

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr)
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }

  const inputDisabled = chatStatus !== 'ready' || isSessionLoading
  const activeStreamingAssistantId =
    chatStatus === 'streaming'
      ? [...messages].reverse().find((message) => message.role === 'assistant')?.id
      : undefined

  return (
    <div className="flex h-[calc(100vh-200px)] flex-col" data-testid="page-chat">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-4">
        <div>
          <h2 className="text-2xl font-semibold text-slate-900">{t('llmChat')}</h2>
          <p className="mt-1 text-sm text-slate-500">{t('chatPageDesc')}</p>
        </div>
        <div className="flex items-center gap-3">
          <button
            onClick={handleNewChat}
            className="rounded-xl border border-sky-200 bg-gradient-to-r from-sky-500 to-blue-500 px-4 py-2 text-sm font-medium text-white shadow-md transition-all hover:shadow-lg hover:shadow-sky-500/25"
          >
            + {t('newSession')}
          </button>
          <select
            value={selectedProvider}
            onChange={(e) => setSelectedProvider(e.target.value)}
            className="rounded-xl border border-sky-100 bg-white px-4 py-2 text-sm text-slate-700 shadow-sm focus:border-sky-300 focus:outline-none focus:ring-2 focus:ring-sky-100"
          >
            {providers.map((provider) => (
              <option key={provider.name} value={provider.name}>
                {provider.name} ({provider.model})
              </option>
            ))}
          </select>
          <button
            onClick={() => {
              window.location.reload()
            }}
            className="rounded-xl border border-slate-200 bg-white px-4 py-2 text-sm text-slate-600 transition-colors hover:bg-slate-50"
          >
            {t('clear')}
          </button>
        </div>
      </div>

      <div className="flex flex-1 gap-4 overflow-hidden">
        <div className="w-64 flex-shrink-0 overflow-y-auto rounded-2xl border border-slate-200 bg-white p-3">
          <div className="mb-2 px-2 text-xs font-semibold uppercase tracking-wide text-slate-400">
            {t('sessions')}
          </div>
          {sessionsData?.sessions && sessionsData.sessions.length > 0 ? (
            <div className="space-y-1">
              {sessionsData.sessions.map((session) => (
                <button
                  key={session.id}
                  onClick={() => handleSelectSession(session.id)}
                  className={`w-full rounded-lg px-3 py-2 text-left text-sm transition-colors ${
                    selectedSessionId === session.id
                      ? 'bg-sky-100 text-sky-700'
                      : 'text-slate-600 hover:bg-slate-50'
                  }`}
                >
                  <div className="truncate font-medium">{session.title || `${session.id.slice(0, 8)}...`}</div>
                  <div className="text-xs text-slate-400">
                    {formatDate(session.created)} · {session.messages} {t('messages')}
                  </div>
                </button>
              ))}
            </div>
          ) : (
            <div className="px-2 py-4 text-center text-xs text-slate-400">
              {t('noSessions')}
            </div>
          )}
        </div>

        <div className={`grid min-w-0 flex-1 gap-4 ${viewMode === 'debug' ? 'xl:grid-cols-[minmax(0,1fr)_340px]' : ''}`}>
          <div className="flex min-w-0 flex-col">
            <div className="glass-panel mb-4 rounded-[28px] p-4">
              <div className="flex flex-wrap items-start justify-between gap-3">
                <div>
                  <div className="flex flex-wrap items-center gap-2 text-xs text-slate-600">
                    <span className="rounded-full bg-sky-100 px-2 py-1 text-sky-800">{t('llmChat')}</span>
                    <span className="rounded-full bg-slate-100 px-2 py-1 text-slate-700">
                      {viewMode === 'debug' ? t('chatViewDebug') : t('chatViewNormal')}
                    </span>
                    <span className="rounded-full bg-white px-2 py-1 text-slate-500">
                      {t('chatToolsSelected', { count: selectedToolNames.length })}
                    </span>
                  </div>
                  <p className="mt-2 text-sm text-slate-600">{t('chatToolSelectionHint')}</p>
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <div className="inline-flex rounded-xl border border-sky-100 bg-white p-1">
                    <button
                      type="button"
                      onClick={() => setViewMode('normal')}
                      className={`rounded-lg px-3 py-1.5 text-sm ${viewMode === 'normal' ? 'bg-blue-600 text-white' : 'text-slate-600'}`}
                    >
                      {t('chatViewNormal')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setViewMode('debug')}
                      className={`rounded-lg px-3 py-1.5 text-sm ${viewMode === 'debug' ? 'bg-blue-600 text-white' : 'text-slate-600'}`}
                    >
                      {t('chatViewDebug')}
                    </button>
                  </div>
                  <button
                    type="button"
                    onClick={armAllTools}
                    disabled={toolsData.length === 0}
                    className="dashboard-secondary-button px-3 py-2 text-sm"
                  >
                    {t('chatArmAllTools')}
                  </button>
                  <button
                    type="button"
                    onClick={clearToolSelection}
                    disabled={selectedToolNames.length === 0}
                    className="dashboard-secondary-button px-3 py-2 text-sm"
                  >
                    {t('chatClearTools')}
                  </button>
                </div>
              </div>

              <div className="mt-4">
                <div className="mb-2 flex items-center justify-between text-xs uppercase tracking-[0.2em] text-slate-400">
                  <span>{t('availableTools')}</span>
                  <span>{t('chatAvailableToolsCount', { count: toolsData.length })}</span>
                </div>
                {isToolsLoading ? (
                  <div className="text-sm text-slate-500">{t('loading')}</div>
                ) : toolsData.length === 0 ? (
                  <div className="rounded-2xl border border-dashed border-slate-200 bg-slate-50/80 px-4 py-3 text-sm text-slate-500">
                    {t('noToolsAvailable')}
                  </div>
                ) : (
                  <div className="flex max-h-36 flex-wrap gap-2 overflow-y-auto pr-1">
                    {toolsData.map((tool) => {
                      const active = selectedToolNames.includes(tool.name)
                      return (
                        <button
                          key={tool.name}
                          type="button"
                          onClick={() => toggleTool(tool.name)}
                          className={`rounded-2xl border px-3 py-2 text-left text-sm transition-colors ${
                            active
                              ? 'border-sky-300 bg-sky-50 text-sky-800'
                              : 'border-slate-200 bg-white text-slate-600 hover:border-sky-200 hover:bg-sky-50/40'
                          }`}
                        >
                          <div className="font-medium">{tool.name}</div>
                          <div className="mt-1 text-xs text-slate-400">{tool.server_name}</div>
                        </button>
                      )
                    })}
                  </div>
                )}
                {selectedToolNames.length === 0 && toolsData.length > 0 && (
                  <div className="mt-3 rounded-2xl border border-dashed border-amber-200 bg-amber-50/60 px-4 py-3 text-sm text-amber-800">
                    {t('chatNoToolsSelected')}
                  </div>
                )}
              </div>
            </div>

            <div className="glass-panel flex-1 overflow-hidden rounded-3xl">
              <div className="max-h-[calc(100vh-470px)] space-y-4 overflow-y-auto p-6">
                {isSessionLoading && (
                  <div className="flex h-32 items-center justify-center">
                    <p className="text-sm text-slate-400">{t('loading')}</p>
                  </div>
                )}
                {!isSessionLoading && messages.length === 0 && (
                  <div className="flex h-32 items-center justify-center">
                    <p className="text-sm text-slate-400">{t('startConversation')}</p>
                  </div>
                )}
                {!isSessionLoading &&
                  messages.map((message) => (
                    <div
                      key={message.id}
                      className={`flex ${message.role === 'user' ? 'justify-end' : 'justify-start'}`}
                    >
                      <div
                        className={`max-w-[85%] rounded-2xl px-5 py-3 ${
                          message.role === 'user'
                            ? 'bg-gradient-to-br from-blue-500 to-blue-600 text-white shadow-lg shadow-blue-500/20'
                            : 'border border-slate-100 bg-white text-slate-800 shadow-sm'
                        }`}
                      >
                        <div className="space-y-3">
                          {message.parts.map((part, index) => renderMessagePart(part, index, viewMode, t))}
                        </div>
                        {message.id === activeStreamingAssistantId && (
                          <span className="ml-1 mt-3 inline-block h-3 w-2 animate-pulse rounded-full bg-slate-400" />
                        )}
                      </div>
                    </div>
                  ))}
                <div ref={messagesEndRef} />
              </div>
            </div>

            <form onSubmit={handleSubmit} className="mt-4 flex gap-3">
              <input
                type="text"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder={t('typeMessage')}
                className="dashboard-input flex-1"
                disabled={inputDisabled}
              />
              <button
                type="submit"
                disabled={inputDisabled || !input.trim()}
                className="dashboard-button px-8"
              >
                {chatStatus === 'streaming' ? t('sending') : t('sendMessage')}
              </button>
            </form>
          </div>

          {viewMode === 'debug' && (
            <aside className="space-y-3" data-testid="chat-debug-panel">
              <div className="dashboard-muted-card rounded-[24px] p-4">
                <div className="mb-2 text-sm font-semibold text-slate-900">{t('chatViewDebug')}</div>
                <p className="text-sm text-slate-600">{t('chatDebugHint')}</p>
              </div>
              <div className="dashboard-muted-card rounded-[24px] p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatProtocolSummary')}</div>
                <div className="space-y-2 text-xs text-slate-600">
                  <div>{t('chatMessagesCount', { count: messages.length })}</div>
                  <div>{t('chatPartsCount', { count: totalParts })}</div>
                  <div>{t('chatCurrentStatus', { status: chatStatus })}</div>
                  <div>{t('chatToolsSelected', { count: selectedToolNames.length })}</div>
                </div>
              </div>
              <div className="dashboard-muted-card rounded-[24px] p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatToolsArmed')}</div>
                {selectedToolNames.length > 0 ? (
                  <div className="space-y-2">
                    {selectedToolNames.map((toolName) => {
                      const tool = toolMap.get(toolName)
                      return (
                        <div key={toolName} className="rounded-2xl border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700">
                          <div className="font-medium text-slate-900">{toolName}</div>
                          <div className="mt-1 text-xs text-slate-500">{tool?.description || tool?.server_name || '-'}</div>
                        </div>
                      )
                    })}
                  </div>
                ) : (
                  <div className="text-sm text-slate-500">{t('chatNoToolsSelected')}</div>
                )}
              </div>
              <div className="dashboard-muted-card rounded-[24px] p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatToolDebugger')}</div>
                {lastAssistantToolParts.length > 0 ? (
                  <pre className="max-h-64 overflow-auto rounded-xl bg-slate-950/95 p-3 text-[11px] text-sky-100">
                    {stringify(lastAssistantToolParts)}
                  </pre>
                ) : (
                  <div className="text-sm text-slate-500">{t('chatNoStructuredData')}</div>
                )}
              </div>
              <div className="dashboard-muted-card rounded-[24px] p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatLastAssistantMessage')}</div>
                {lastAssistant ? (
                  <pre className="max-h-64 overflow-auto rounded-xl bg-slate-950/95 p-3 text-[11px] text-sky-100">
                    {stringify({
                      id: lastAssistant.id,
                      metadata: lastAssistant.metadata ?? null,
                      parts: lastAssistant.parts ?? [],
                    })}
                  </pre>
                ) : (
                  <div className="text-sm text-slate-500">{t('chatNoStructuredData')}</div>
                )}
              </div>
            </aside>
          )}
        </div>
      </div>
    </div>
  )
}
