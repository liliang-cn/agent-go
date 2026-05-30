import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useConfig, useUpdateConfig } from '../hooks/useApi'

export function Settings() {
  const { t, i18n } = useTranslation()
  const { data: config, isLoading, error } = useConfig()
  const updateConfigMutation = useUpdateConfig()
  const [saved, setSaved] = useState(false)

  const [homeDir, setHomeDir] = useState('')
  const [debug, setDebug] = useState(false)
  const [serverHost, setServerHost] = useState('')
  const [serverPort, setServerPort] = useState('7127')
  const [memoryStoreType, setMemoryStoreType] = useState('')

  useEffect(() => {
    if (!config) return
    setHomeDir(config.home || '')
    setDebug(Boolean(config.debug))
    setServerHost(config.serverHost || '')
    setServerPort(String(config.serverPort || 7127))
    setMemoryStoreType(config.memoryStoreType || '')
  }, [config])

  const handleSave = async (event: React.FormEvent) => {
    event.preventDefault()
    try {
      await updateConfigMutation.mutateAsync({
        home: homeDir,
        debug,
        serverHost,
        serverPort: Number(serverPort),
        memoryStoreType,
      })
      setSaved(true)
      setTimeout(() => setSaved(false), 3000)
    } catch (mutationError) {
      alert(`${t('error')}: ${mutationError instanceof Error ? mutationError.message : 'Unknown error'}`)
    }
  }

  if (isLoading) {
    return <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6 text-muted-foreground">{t('loading')}</div>
  }

  if (error) {
    return <div className="rounded-lg border border-rose-200 bg-rose-50 p-6 text-rose-700">{t('error')}: {error.message}</div>
  }

  return (
    <div className="space-y-6" data-testid="page-settings">
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
        <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('projectConfiguration')}</p>
        <h2 className="mt-2 text-3xl font-semibold text-foreground">{t('settings')}</h2>
        <p className="mt-3 max-w-3xl text-sm leading-7 text-muted-foreground">
          {t('settingsIntro')}
        </p>
      </div>

      <form onSubmit={handleSave} className="grid gap-6 xl:grid-cols-[minmax(0,1.1fr)_380px]" data-testid="settings-form">
        <div className="space-y-6">
          <section className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('core')}</p>
            <div className="mt-5 grid gap-4 md:grid-cols-2">
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('home')}</span>
                <input value={homeDir} onChange={(e) => setHomeDir(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('debugLabel')}</span>
                <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-[18px] px-4 py-3 text-foreground">
                  <label className="flex items-center gap-3">
                    <input type="checkbox" checked={debug} onChange={(e) => setDebug(e.target.checked)} />
                    {t('enableVerboseRuntimeLogging')}
                  </label>
                </div>
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('serverHost')}</span>
                <input value={serverHost} onChange={(e) => setServerHost(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('serverPort')}</span>
                <input value={serverPort} onChange={(e) => setServerPort(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
            </div>
          </section>

          <section className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('knowledgeAndMemory')}</p>
            <div className="mt-5 grid gap-4 md:grid-cols-2">
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('memoryStoreType')}</span>
                <input value={memoryStoreType} onChange={(e) => setMemoryStoreType(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
            </div>
          </section>

          <div className="flex items-center gap-4">
            <button type="submit" disabled={updateConfigMutation.isPending} className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50">
              {updateConfigMutation.isPending ? t('loading') : t('saveSettings')}
            </button>
            {saved && <span className="text-emerald-600">{t('settingsSaved')}</span>}
          </div>
        </div>

        <aside className="space-y-6">
          <section className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('sourceOfTruth')}</p>
            <dl className="mt-5 space-y-4 text-sm">
              <div>
                <dt className="text-muted-foreground">Agent DB</dt>
                <dd className="mt-1 break-all text-foreground">{config?.agentDbPath}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('dataDir')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.dataDir}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('workspaceDir')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.workspaceDir}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('filesystemAllowlist')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.mcpAllowedDirs?.join(', ') || '-'}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('ragDatabasePath')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.ragDbPath}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('memoryPath')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.memoryPath}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('skillsPaths')}</dt>
                <dd className="mt-1 break-all text-foreground whitespace-pre-line">{config?.skillsPaths?.join('\n') || '-'}</dd>
              </div>
              <div>
                <dt className="text-muted-foreground">{t('mcpServersFile')}</dt>
                <dd className="mt-1 break-all text-foreground">{config?.mcpServersPath}</dd>
              </div>
            </dl>
          </section>

          <section className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('language')}</p>
            <div className="mt-5 flex gap-3">
              <button
                type="button"
                onClick={() => i18n.changeLanguage('zh')}
                className={i18n.language === 'zh' ? 'inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50' : 'inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-3 text-sm'}
              >
                中文
              </button>
              <button
                type="button"
                onClick={() => i18n.changeLanguage('en')}
                className={i18n.language === 'en' ? 'inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50' : 'inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-3 text-sm'}
              >
                English
              </button>
            </div>
          </section>
        </aside>
      </form>
    </div>
  )
}
