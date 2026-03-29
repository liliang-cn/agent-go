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
    return <div className="glass-panel rounded-[28px] p-6 text-slate-600">{t('loading')}</div>
  }

  if (error) {
    return <div className="rounded-[28px] border border-rose-200 bg-rose-50 p-6 text-rose-700">{t('error')}: {error.message}</div>
  }

  return (
    <div className="space-y-6" data-testid="page-settings">
      <div className="glass-panel rounded-[32px] p-6">
        <p className="text-xs uppercase tracking-[0.28em] text-slate-500">{t('projectConfiguration')}</p>
        <h2 className="mt-2 text-3xl font-semibold text-slate-900">{t('settings')}</h2>
        <p className="mt-3 max-w-3xl text-sm leading-7 text-slate-600">
          {t('settingsIntro')}
        </p>
      </div>

      <form onSubmit={handleSave} className="grid gap-6 xl:grid-cols-[minmax(0,1.1fr)_380px]" data-testid="settings-form">
        <div className="space-y-6">
          <section className="glass-panel rounded-[32px] p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-slate-500">{t('core')}</p>
            <div className="mt-5 grid gap-4 md:grid-cols-2">
              <label className="space-y-2">
                <span className="text-sm font-medium text-slate-700">{t('home')}</span>
                <input value={homeDir} onChange={(e) => setHomeDir(e.target.value)} className="dashboard-input" />
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-slate-700">{t('debugLabel')}</span>
                <div className="dashboard-muted-card rounded-[18px] px-4 py-3 text-slate-700">
                  <label className="flex items-center gap-3">
                    <input type="checkbox" checked={debug} onChange={(e) => setDebug(e.target.checked)} />
                    {t('enableVerboseRuntimeLogging')}
                  </label>
                </div>
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-slate-700">{t('serverHost')}</span>
                <input value={serverHost} onChange={(e) => setServerHost(e.target.value)} className="dashboard-input" />
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-slate-700">{t('serverPort')}</span>
                <input value={serverPort} onChange={(e) => setServerPort(e.target.value)} className="dashboard-input" />
              </label>
            </div>
          </section>

          <section className="glass-panel rounded-[32px] p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-slate-500">{t('knowledgeAndMemory')}</p>
            <div className="mt-5 grid gap-4 md:grid-cols-2">
              <label className="space-y-2">
                <span className="text-sm font-medium text-slate-700">{t('memoryStoreType')}</span>
                <input value={memoryStoreType} onChange={(e) => setMemoryStoreType(e.target.value)} className="dashboard-input" />
              </label>
            </div>
          </section>

          <div className="flex items-center gap-4">
            <button type="submit" disabled={updateConfigMutation.isPending} className="dashboard-button">
              {updateConfigMutation.isPending ? t('loading') : t('saveSettings')}
            </button>
            {saved && <span className="text-emerald-600">{t('settingsSaved')}</span>}
          </div>
        </div>

        <aside className="space-y-6">
          <section className="glass-panel rounded-[32px] p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-slate-500">{t('sourceOfTruth')}</p>
            <dl className="mt-5 space-y-4 text-sm">
              <div>
                <dt className="text-slate-500">Agent DB</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.agentDbPath}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('dataDir')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.dataDir}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('workspaceDir')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.workspaceDir}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('filesystemAllowlist')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.mcpAllowedDirs?.join(', ') || '-'}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('ragDatabasePath')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.ragDbPath}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('memoryPath')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.memoryPath}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('skillsPaths')}</dt>
                <dd className="mt-1 break-all text-slate-900 whitespace-pre-line">{config?.skillsPaths?.join('\n') || '-'}</dd>
              </div>
              <div>
                <dt className="text-slate-500">{t('mcpServersFile')}</dt>
                <dd className="mt-1 break-all text-slate-900">{config?.mcpServersPath}</dd>
              </div>
            </dl>
          </section>

          <section className="glass-panel rounded-[32px] p-6">
            <p className="text-xs uppercase tracking-[0.28em] text-slate-500">{t('language')}</p>
            <div className="mt-5 flex gap-3">
              <button
                type="button"
                onClick={() => i18n.changeLanguage('zh')}
                className={i18n.language === 'zh' ? 'dashboard-button' : 'dashboard-secondary-button px-4 py-3 text-sm'}
              >
                中文
              </button>
              <button
                type="button"
                onClick={() => i18n.changeLanguage('en')}
                className={i18n.language === 'en' ? 'dashboard-button' : 'dashboard-secondary-button px-4 py-3 text-sm'}
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
