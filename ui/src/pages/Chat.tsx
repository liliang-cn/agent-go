import { useChat } from '@ai-sdk/react'
import { DefaultChatTransport } from 'ai'
import { useTranslation } from 'react-i18next'
import { useState, useEffect, useMemo } from 'react'
import { useStatus, useChatSessions, useChatSession } from '../hooks/useApi'

export function Chat() {
  const { t } = useTranslation()
  const { data: status } = useStatus()
  const { data: sessionsData } = useChatSessions(20)
  const [selectedProvider, setSelectedProvider] = useState('')
  const [selectedSessionId, setSelectedSessionId] = useState<string | undefined>(undefined)
  const [input, setInput] = useState('')

  const { data: sessionDetail } = useChatSession(selectedSessionId)

  // Get available providers from status
  const providers = status?.providers?.filter(p => p.status === 'enabled' && p.type === 'llm') || []

  // Set default provider
  useEffect(() => {
    if (providers.length > 0 && !selectedProvider) {
      setSelectedProvider(providers[0].name)
    }
  }, [providers, selectedProvider])

  // Load session messages when session is selected
  useEffect(() => {
    if (sessionDetail?.messages && sessionDetail.messages.length > 0) {
      // Convert session messages to UI format and set them
      const loadedMessages = sessionDetail.messages.map((msg, idx) => ({
        id: msg.id || `msg-${idx}`,
        role: msg.role as 'user' | 'assistant',
        parts: [{ type: 'text' as const, text: msg.content }],
      }))
      setChatMessages(loadedMessages)
    }
  }, [sessionDetail])

  // Create transport with current provider and session
  const transport = useMemo(() => new DefaultChatTransport({
    api: '/api/chat',
    body: {
      mode: 'llm',
      provider: selectedProvider,
      id: selectedSessionId,
    },
  }), [selectedProvider, selectedSessionId])

  const [chatMessages, setChatMessages] = useState<Array<{ id: string; role: 'user' | 'assistant' | 'system'; parts: Array<{ type: 'text'; text: string }> }>>([])

  const { sendMessage, status: chatStatus, messages: aiMessages, setMessages: setAiMessages } = useChat({
    transport,
  })

  // Use loaded session messages or AI SDK messages
  const messages = chatMessages.length > 0 ? chatMessages : aiMessages as typeof chatMessages

  // Handle session selection
  const handleSelectSession = (sessionId: string) => {
    setSelectedSessionId(sessionId)
    setChatMessages([])
  }

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault()
    if (input.trim() && chatStatus === 'ready') {
      sendMessage({ text: input })
      setInput('')
    }
  }

  const handleNewChat = () => {
    setSelectedSessionId(undefined)
    setChatMessages([])
    setInput('')
  }

  const formatDate = (dateStr: string) => {
    const date = new Date(dateStr)
    return date.toLocaleDateString() + ' ' + date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  }

  return (
    <div className="flex h-[calc(100vh-200px)] flex-col" data-testid="page-chat">
      {/* Header */}
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
        {/* Sessions Sidebar */}
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
                  <div className="truncate font-medium">{session.id.slice(0, 8)}...</div>
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

        {/* Chat Area */}
        <div className="flex flex-1 flex-col">
          {/* Messages */}
          <div className="glass-panel flex-1 overflow-hidden rounded-3xl">
            <div className="max-h-[calc(100vh-420px)] space-y-4 overflow-y-auto p-6">
              {messages.length === 0 && (
                <div className="flex h-32 items-center justify-center">
                  <p className="text-sm text-slate-400">{t('startConversation')}</p>
                </div>
              )}
              {messages.map((message) => (
                <div
                  key={message.id}
                  className={`flex ${message.role === 'user' ? 'justify-end' : 'justify-start'}`}
                >
                  <div
                    className={`max-w-[75%] rounded-2xl px-5 py-3 ${
                      message.role === 'user'
                        ? 'bg-gradient-to-br from-blue-500 to-blue-600 text-white shadow-lg shadow-blue-500/20'
                        : 'border border-slate-100 bg-white text-slate-800 shadow-sm'
                    }`}
                  >
                    <div className="whitespace-pre-wrap leading-relaxed">
                      {message.parts.map((part, index) =>
                        part.type === 'text' ? <span key={index}>{part.text}</span> : null,
                      )}
                    </div>
                    {message.role === 'assistant' && chatStatus === 'streaming' && (
                      <span className="ml-1 inline-block h-3 w-2 animate-pulse rounded-full bg-slate-400" />
                    )}
                  </div>
                </div>
              ))}
            </div>
          </div>

          {/* Input */}
          <form onSubmit={handleSubmit} className="mt-4 flex gap-3">
            <input
              type="text"
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder={t('typeMessage')}
              className="dashboard-input flex-1"
              disabled={chatStatus !== 'ready'}
            />
            <button
              type="submit"
              disabled={chatStatus !== 'ready' || !input.trim()}
              className="dashboard-button px-8"
            >
              {chatStatus === 'streaming' ? t('sending') : t('sendMessage')}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
