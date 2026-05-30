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
      className="rounded-lg border border-amber-200 bg-amber-50/70 p-3 text-sm text-slate-700"
    >
      <summary className="flex cursor-pointer list-none items-center justify-between gap-3">
        <span className="font-mono text-[13px] font-medium text-slate-900">{title}</span>
        <span className="rounded-full border border-amber-200 bg-white px-2 py-0.5 font-mono text-[10px] text-slate-500">
          {part.state}
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
      <details key={`reasoning-${index}`} className="rounded-lg border border-blue-100 bg-blue-50/60 p-3 text-sm text-slate-700">
        <summary className="cursor-pointer list-none font-medium text-slate-900">{t('chatReasoning')}</summary>
        <pre className="mt-3 overflow-x-auto whitespace-pre-wrap rounded-md bg-white/80 p-3 font-mono text-[11px] text-slate-700">
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
    <pre key={`part-${index}`} className="overflow-x-auto whitespace-pre-wrap rounded-md bg-[#18181b] p-3 font-mono text-[11px] text-slate-100">
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
  const [toolsOpen, setToolsOpen] = useState(false)

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
    <div className="flex h-[calc(100vh-160px)] flex-col" data-testid="page-chat">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 className="text-2xl font-bold tracking-tight text-foreground">{t('llmChat')}</h2>
          <p className="mt-0.5 text-sm text-muted-foreground">{t('chatPageDesc')}</p>
        </div>
        <div className="flex items-center gap-2">
          <select
            value={selectedProvider}
            onChange={(e) => setSelectedProvider(e.target.value)}
            className="rounded-[7px] border border-input bg-white px-3 py-2 font-mono text-[13px] text-foreground focus:border-ring focus:outline-none"
          >
            {providers.map((provider) => (
              <option key={provider.name} value={provider.name}>
                {provider.name} ({provider.model})
              </option>
            ))}
          </select>
          <button onClick={handleNewChat} className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-4 py-2">
            + {t('newSession')}
          </button>
          <button onClick={() => window.location.reload()} className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2">
            {t('clear')}
          </button>
        </div>
      </div>

      <div className="flex flex-1 gap-4 overflow-hidden">
        <aside className="hidden w-56 flex-shrink-0 flex-col overflow-y-auto rounded-[10px] border border-border bg-white p-2 sm:flex">
          <div className="px-2 py-1.5 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">
            {t('sessions')}
          </div>
          {sessionsData?.sessions && sessionsData.sessions.length > 0 ? (
            <div className="space-y-0.5">
              {sessionsData.sessions.map((session) => {
                const active = selectedSessionId === session.id
                return (
                  <button
                    key={session.id}
                    onClick={() => handleSelectSession(session.id)}
                    className={`w-full rounded-md border-l-2 px-2.5 py-2 text-left text-sm transition-colors ${
                      active
                        ? 'border-primary bg-muted text-foreground'
                        : 'border-transparent text-muted-foreground hover:bg-muted'
                    }`}
                  >
                    <div className="truncate font-medium">{session.title || `${session.id.slice(0, 8)}…`}</div>
                    <div className="mt-0.5 font-mono text-[10px] text-muted-foreground">
                      {formatDate(session.created)} · {session.messages}
                    </div>
                  </button>
                )
              })}
            </div>
          ) : (
            <div className="px-2 py-4 text-center text-xs text-muted-foreground">
              {t('noSessions')}
            </div>
          )}
        </aside>

        <div className={`grid min-w-0 flex-1 gap-4 ${viewMode === 'debug' ? 'xl:grid-cols-[minmax(0,1fr)_340px]' : ''}`}>
          <div className="flex min-w-0 flex-col gap-3">
            {/* Compact tools bar — collapsed by default so messages get the space */}
            <div className="rounded-[10px] border border-border bg-white">
              <div className="flex flex-wrap items-center justify-between gap-2 px-3 py-2">
                <button
                  type="button"
                  onClick={() => setToolsOpen((v) => !v)}
                  className="flex items-center gap-2 text-sm font-medium text-foreground"
                >
                  <span className={`inline-block transition-transform ${toolsOpen ? 'rotate-90' : ''}`}>›</span>
                  {t('availableTools')}
                  <span className="rounded-full bg-muted px-2 py-0.5 font-mono text-[11px] text-muted-foreground">
                    {selectedToolNames.length}/{toolsData.length}
                  </span>
                </button>
                <div className="flex items-center gap-2">
                  <div className="inline-flex rounded-md border border-border bg-white p-0.5">
                    <button
                      type="button"
                      onClick={() => setViewMode('normal')}
                      className={`rounded-[5px] px-2.5 py-1 text-xs font-medium transition ${viewMode === 'normal' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
                    >
                      {t('chatViewNormal')}
                    </button>
                    <button
                      type="button"
                      onClick={() => setViewMode('debug')}
                      className={`rounded-[5px] px-2.5 py-1 text-xs font-medium transition ${viewMode === 'debug' ? 'bg-primary text-primary-foreground' : 'text-muted-foreground'}`}
                    >
                      {t('chatViewDebug')}
                    </button>
                  </div>
                  <button
                    type="button"
                    onClick={armAllTools}
                    disabled={toolsData.length === 0}
                    className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2 px-2.5 py-1.5 text-xs"
                  >
                    {t('chatArmAllTools')}
                  </button>
                  <button
                    type="button"
                    onClick={clearToolSelection}
                    disabled={selectedToolNames.length === 0}
                    className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2 px-2.5 py-1.5 text-xs"
                  >
                    {t('chatClearTools')}
                  </button>
                </div>
              </div>
              {toolsOpen && (
                <div className="border-t border-border p-3">
                  {isToolsLoading ? (
                    <div className="text-sm text-muted-foreground">{t('loading')}</div>
                  ) : toolsData.length === 0 ? (
                    <div className="rounded-md border border-dashed border-input bg-muted px-3 py-2 text-sm text-muted-foreground">
                      {t('noToolsAvailable')}
                    </div>
                  ) : (
                    <div className="flex max-h-44 flex-wrap gap-1.5 overflow-y-auto pr-1">
                      {toolsData.map((tool) => {
                        const active = selectedToolNames.includes(tool.name)
                        return (
                          <button
                            key={tool.name}
                            type="button"
                            onClick={() => toggleTool(tool.name)}
                            title={tool.server_name}
                            className={`rounded-md border px-2.5 py-1.5 text-left font-mono text-[12px] transition-colors ${
                              active
                                ? 'border-primary bg-primary text-primary-foreground'
                                : 'border-input bg-white text-muted-foreground hover:border-muted-foreground'
                            }`}
                          >
                            {tool.name}
                          </button>
                        )
                      })}
                    </div>
                  )}
                </div>
              )}
            </div>

            <div className="flex-1 overflow-hidden rounded-[10px] border border-border bg-white">
              <div className="h-full space-y-4 overflow-y-auto p-5">
                {isSessionLoading && (
                  <div className="flex h-32 items-center justify-center">
                    <p className="text-sm text-muted-foreground">{t('loading')}</p>
                  </div>
                )}
                {!isSessionLoading && messages.length === 0 && (
                  <div className="flex h-full min-h-32 flex-col items-center justify-center gap-1 text-center">
                    <p className="text-sm text-muted-foreground">{t('startConversation')}</p>
                  </div>
                )}
                {!isSessionLoading &&
                  messages.map((message) => (
                    <div
                      key={message.id}
                      className={`flex ${message.role === 'user' ? 'justify-end' : 'justify-start'}`}
                    >
                      <div
                        className={`max-w-[85%] rounded-[10px] px-4 py-2.5 text-sm ${
                          message.role === 'user'
                            ? 'bg-primary text-primary-foreground'
                            : 'border border-border bg-muted text-foreground'
                        }`}
                      >
                        <div className="space-y-3">
                          {message.parts.map((part, index) => renderMessagePart(part, index, viewMode, t))}
                        </div>
                        {message.id === activeStreamingAssistantId && (
                          <span className="ml-1 mt-3 inline-block h-3 w-1.5 animate-pulse rounded-full bg-muted-foreground" />
                        )}
                      </div>
                    </div>
                  ))}
                <div ref={messagesEndRef} />
              </div>
            </div>

            <form onSubmit={handleSubmit} className="flex gap-2">
              <input
                type="text"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                placeholder={t('typeMessage')}
                className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 flex-1"
                disabled={inputDisabled}
              />
              <button
                type="submit"
                disabled={inputDisabled || !input.trim()}
                className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-4 py-2 px-6"
              >
                {chatStatus === 'streaming' ? t('sending') : t('sendMessage')}
              </button>
            </form>
          </div>

          {viewMode === 'debug' && (
            <aside className="space-y-3" data-testid="chat-debug-panel">
              <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                <div className="mb-2 text-sm font-semibold text-slate-900">{t('chatViewDebug')}</div>
                <p className="text-sm text-slate-600">{t('chatDebugHint')}</p>
              </div>
              <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatProtocolSummary')}</div>
                <div className="space-y-2 text-xs text-slate-600">
                  <div>{t('chatMessagesCount', { count: messages.length })}</div>
                  <div>{t('chatPartsCount', { count: totalParts })}</div>
                  <div>{t('chatCurrentStatus', { status: chatStatus })}</div>
                  <div>{t('chatToolsSelected', { count: selectedToolNames.length })}</div>
                </div>
              </div>
              <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
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
              <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatToolDebugger')}</div>
                {lastAssistantToolParts.length > 0 ? (
                  <pre className="max-h-64 overflow-auto rounded-xl bg-[#18181b] p-3 text-[11px] text-slate-100">
                    {stringify(lastAssistantToolParts)}
                  </pre>
                ) : (
                  <div className="text-sm text-slate-500">{t('chatNoStructuredData')}</div>
                )}
              </div>
              <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                <div className="mb-3 text-sm font-semibold text-slate-900">{t('chatLastAssistantMessage')}</div>
                {lastAssistant ? (
                  <pre className="max-h-64 overflow-auto rounded-xl bg-[#18181b] p-3 text-[11px] text-slate-100">
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
