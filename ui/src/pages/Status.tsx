import { useTranslation } from 'react-i18next'
import { useStatus } from '../hooks/useApi'

interface Provider {
  name: string
  status: string
  type: string
  model?: string
  healthy?: boolean
  active_requests?: number
  max_concurrency?: number
  capability?: number
}

function translateStatus(value: string, t: (key: string) => string) {
  switch (value) {
    case 'enabled':
      return t('enabled')
    case 'disabled':
      return t('disabled')
    case 'running':
      return t('running')
    case 'stopped':
      return t('stopped')
    default:
      return value
  }
}

function ProviderCard({ provider, statusColor }: { provider: Provider; statusColor: string }) {
  const { t } = useTranslation()

  return (
    <div className="dashboard-muted-card rounded-[24px] p-4">
      <div className="mb-2 flex items-center justify-between">
        <h4 className="font-medium text-slate-900">{provider.name}</h4>
        <div className="flex items-center gap-2">
          <div className={`h-3 w-3 rounded-full ${statusColor}`} />
          <span className="text-sm capitalize text-slate-500">{translateStatus(provider.status, t)}</span>
        </div>
      </div>
      {provider.model && <p className="text-sm text-slate-500">{t('modelLabel', { model: provider.model })}</p>}
      {(provider.capability !== undefined || provider.max_concurrency !== undefined || provider.active_requests !== undefined) && (
        <dl className="mt-3 grid grid-cols-3 gap-3 text-xs">
          <div>
            <dt className="text-slate-400">{t('capabilityLabel')}</dt>
            <dd className="mt-1 text-slate-700">{provider.capability ?? '-'}</dd>
          </div>
          <div>
            <dt className="text-slate-400">{t('maxConcurrent')}</dt>
            <dd className="mt-1 text-slate-700">{provider.max_concurrency ?? '-'}</dd>
          </div>
          <div>
            <dt className="text-slate-400">{t('activeRequests')}</dt>
            <dd className="mt-1 text-slate-700">{provider.active_requests ?? 0}</dd>
          </div>
        </dl>
      )}
    </div>
  )
}

export function Status() {
  const { t } = useTranslation()
  const { data, isLoading, error, refetch } = useStatus()

  if (isLoading) {
    return (
      <div className="flex h-64 items-center justify-center" data-testid="page-status">
        <div className="h-8 w-8 animate-spin rounded-full border-b-2 border-blue-600" />
      </div>
    )
  }

  if (error) {
    return (
      <div className="rounded-[24px] border border-rose-200 bg-rose-50 p-4" data-testid="page-status">
        <p className="text-rose-700">{t('errorLoadingStatus')}: {error.message}</p>
        <button onClick={() => refetch()} className="dashboard-button mt-2 px-4 py-2">
          {t('retry')}
        </button>
      </div>
    )
  }

  const getStatusColor = (status: string) => {
    switch (status) {
      case 'enabled':
        return 'bg-emerald-500'
      case 'disabled':
        return 'bg-rose-500'
      default:
        return 'bg-slate-400'
    }
  }

  const llmProviders = data?.providers?.filter((p: Provider) => p.type === 'llm') || []
  const embedProviders = data?.providers?.filter((p: Provider) => p.type === 'embedding' && p.status === 'enabled') || []
  const otherProviders = data?.providers?.filter((p: Provider) => !['llm', 'embedding'].includes(p.type)) || []

  return (
    <div className="space-y-6" data-testid="page-status">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-slate-900">{t('systemStatus')}</h2>
        <button onClick={() => refetch()} className="dashboard-secondary-button px-4 py-2 text-sm" data-testid="status-refresh">
          {t('refresh')}
        </button>
      </div>

      {data && (
        <>
          <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
            <div className="glass-panel rounded-[28px] p-6">
              <h3 className="mb-1 text-sm font-medium text-slate-500">{t('statusLabel')}</h3>
              <p className="text-2xl font-semibold text-slate-900">{translateStatus(data.status, t)}</p>
            </div>
            <div className="glass-panel rounded-[28px] p-6">
              <h3 className="mb-1 text-sm font-medium text-slate-500">{t('version')}</h3>
              <p className="text-2xl font-semibold text-slate-900">{data.version}</p>
            </div>
          </div>

          {llmProviders.length > 0 && (
            <div data-testid="status-llm-providers">
              <h3 className="mb-4 text-lg font-medium text-slate-900">{t('llmProviders')}</h3>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                {llmProviders.map((provider: Provider) => (
                  <ProviderCard key={provider.name} provider={provider} statusColor={getStatusColor(provider.status)} />
                ))}
              </div>
            </div>
          )}

          {embedProviders.length > 0 && (
            <div data-testid="status-embedding-providers">
              <h3 className="mb-4 text-lg font-medium text-slate-900">{t('embeddingProviders')}</h3>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                {embedProviders.map((provider: Provider) => (
                  <ProviderCard key={provider.name} provider={provider} statusColor={getStatusColor(provider.status)} />
                ))}
              </div>
            </div>
          )}

          {otherProviders.length > 0 && (
            <div data-testid="status-services">
              <h3 className="mb-4 text-lg font-medium text-slate-900">{t('services')}</h3>
              <div className="grid grid-cols-1 gap-4 md:grid-cols-2 lg:grid-cols-3">
                {otherProviders.map((provider: Provider) => (
                  <div key={provider.name} className="dashboard-muted-card rounded-[24px] p-4">
                    <div className="mb-2 flex items-center justify-between">
                      <h4 className="font-medium text-slate-900">{provider.name}</h4>
                      <div className="flex items-center gap-2">
                        <div className={`h-3 w-3 rounded-full ${getStatusColor(provider.status)}`} />
                        <span className="text-sm capitalize text-slate-500">{translateStatus(provider.status, t)}</span>
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </div>
          )}

          {data.mcp?.enabled && data.mcp?.server_list && (
            <div data-testid="status-mcp-servers">
              <h3 className="mb-4 text-lg font-medium text-slate-900">
                {t('mcpServers')} {t('mcpServersSummary', { servers: data.mcp.servers, tools: data.mcp.tools })}
              </h3>
              <div className="space-y-3">
                {data.mcp.server_list.map((server: any) => (
                  <div key={server.name} className="dashboard-muted-card rounded-[24px] p-4">
                    <div className="mb-2 flex items-center justify-between">
                      <h4 className="font-medium text-slate-900">{server.name}</h4>
                      <span className={`rounded px-2 py-1 text-xs ${server.running ? 'bg-emerald-100 text-emerald-700' : 'bg-slate-100 text-slate-700'}`}>
                        {server.running ? t('running') : t('stopped')}
                      </span>
                    </div>
                    <p className="text-sm text-slate-500">{t('toolsSummary', { count: server.tool_count })}</p>
                  </div>
                ))}
              </div>
            </div>
          )}

          {data.rag?.enabled && (data.rag.documents ?? 0) > 0 && (
            <div className="glass-panel rounded-[24px] p-4" data-testid="status-rag">
              <h3 className="mb-2 text-lg font-medium text-slate-900">{t('ragDatabase')}</h3>
              <p className="text-sm text-slate-500">
                {t('documentsCount')}: {data.rag.documents} | {t('chunks')}: {data.rag.chunks}
              </p>
              <p className="mt-1 text-xs text-slate-400">{data.rag.db_path}</p>
            </div>
          )}

          {data.memory?.enabled && (
            <div className="glass-panel rounded-[24px] p-4" data-testid="status-memory">
              <h3 className="mb-2 text-lg font-medium text-slate-900">{t('memoryNav')}</h3>
              <p className="text-sm text-slate-500">{t('memoriesCount')}: {data.memory.count}</p>
            </div>
          )}

          {data.skills?.enabled && (
            <div className="glass-panel rounded-[24px] p-4" data-testid="status-skills">
              <h3 className="mb-2 text-lg font-medium text-slate-900">{t('skills')}</h3>
              <p className="text-sm text-slate-500">{t('loadedCount', { count: data.skills.count })}</p>
            </div>
          )}
        </>
      )}
    </div>
  )
}
