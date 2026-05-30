import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useApplySetup, useSetup } from '../hooks/useApi'

function derivePath(home: string, relative: string) {
  const trimmedHome = home.trim()
  if (!trimmedHome) return ''
  return `${trimmedHome.replace(/\/+$/, '')}/${relative}`
}

function deriveMemoryPath(home: string, memoryStoreType: string) {
  const cortexPath = derivePath(home, 'data/cortex.db')
  if (memoryStoreType === 'cortex') {
    return cortexPath
  }
  return derivePath(home, 'data/memories')
}

const STEP_COUNT = 4

export function Setup() {
  const { t } = useTranslation()
  const { data, isLoading, error } = useSetup()
  const applySetup = useApplySetup()
  const [step, setStep] = useState(0)
  const [saved, setSaved] = useState(false)

  const [home, setHome] = useState('')
  const [serverHost, setServerHost] = useState('127.0.0.1')
  const [serverPort, setServerPort] = useState('7127')
  const [memoryStoreType, setMemoryStoreType] = useState('file')
  const [providerName, setProviderName] = useState('local')
  const [providerBaseUrl, setProviderBaseUrl] = useState('http://127.0.0.1:11434/v1')
  const [apiKey, setApiKey] = useState('')
  const [modelName, setModelName] = useState('')
  const [embeddingModel, setEmbeddingModel] = useState('')
  const [maxConcurrency, setMaxConcurrency] = useState('5')
  const [capability, setCapability] = useState('4')
  const [showOptionalKnowledge, setShowOptionalKnowledge] = useState(false)

  useEffect(() => {
    if (!data) return
    const firstProvider = data.providers[0]
    setHome(data.home || '')
    setServerHost(data.serverHost || '127.0.0.1')
    setServerPort(String(data.serverPort || 7127))
    setMemoryStoreType(data.memoryStoreType || 'file')
    setProviderName(firstProvider?.name || 'local')
    setProviderBaseUrl(firstProvider?.baseUrl || 'http://127.0.0.1:11434/v1')
    setModelName(firstProvider?.modelName || '')
    setEmbeddingModel(firstProvider?.embeddingModel || '')
    setMaxConcurrency(String(firstProvider?.maxConcurrency || 5))
    setCapability(String(firstProvider?.capability || 4))
  }, [data])

  const derivedWorkspace = useMemo(() => derivePath(home, 'workspace'), [home])
  const derivedRagDb = useMemo(() => derivePath(home, 'data/cortex.db'), [home])
  const derivedMemoryPath = useMemo(() => deriveMemoryPath(home, memoryStoreType), [home, memoryStoreType])
  const reviewItems = useMemo(
    () => {
      const items: Array<[string, string]> = [
        [t('home'), home],
        [t('workingDirectory'), derivedWorkspace],
        [t('serverHost'), serverHost],
        [t('serverPort'), serverPort],
        [t('memoryStoreType'), memoryStoreType || '-'],
        [t('memoryPath'), derivedMemoryPath || '-'],
      ]
      if (embeddingModel.trim()) {
        items.push([t('ragDatabasePath'), derivedRagDb || '-'])
      }
      return items
    },
    [t, home, derivedWorkspace, serverHost, serverPort, derivedRagDb, memoryStoreType, derivedMemoryPath, embeddingModel],
  )

  const providerItems = useMemo(
    () => {
      const items: Array<[string, string]> = [
        [t('providerName'), providerName],
        [t('providerBaseUrl'), providerBaseUrl],
        [t('modelName'), modelName],
        [t('maxConcurrency'), maxConcurrency],
        [t('capabilityLevel'), capability],
      ]
      if (embeddingModel.trim()) {
        items.push([t('embeddingModel'), embeddingModel])
      }
      return items
    },
    [t, providerName, providerBaseUrl, modelName, embeddingModel, maxConcurrency, capability],
  )

  const handleApply = async () => {
    await applySetup.mutateAsync({
      home,
      serverHost,
      serverPort: Number(serverPort),
      memoryStoreType,
      provider: {
        name: providerName,
        baseUrl: providerBaseUrl,
        apiKey,
        modelName,
        embeddingModel,
        maxConcurrency: Number(maxConcurrency) || 5,
        capability: Number(capability) || 4,
      },
    })
    setSaved(true)
  }

  if (isLoading) {
    return <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6 text-muted-foreground">{t('loading')}</div>
  }

  if (error) {
    return <div className="rounded-lg border border-rose-200 bg-rose-50 p-6 text-rose-700">{t('error')}: {error.message}</div>
  }

  return (
    <div className="space-y-6" data-testid="page-setup">
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-start lg:justify-between">
          <div>
            <p className="text-xs uppercase tracking-[0.28em] text-muted-foreground">{t('setupStep', { current: step + 1, total: STEP_COUNT })}</p>
            <h2 className="mt-2 text-3xl font-semibold text-foreground">{t('setupTitle')}</h2>
            <p className="mt-3 max-w-3xl text-sm leading-7 text-muted-foreground">{t('setupIntro')}</p>
          </div>
          <div className={`rounded-full px-4 py-2 text-sm ${data?.initialized ? 'bg-emerald-50 text-emerald-700' : 'bg-amber-50 text-amber-700'}`}>
            {data?.initialized ? t('setupStatusReady') : t('setupStatusPending')}
          </div>
        </div>
      </div>

      <div className="grid gap-6 lg:grid-cols-[240px_minmax(0,1fr)]">
        <aside className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-4">
          {[
            t('setupWorkspace'),
            t('setupProvider'),
            t('setupFeatures'),
            t('setupReview'),
          ].map((label, index) => (
            <button
              key={label}
              type="button"
              onClick={() => setStep(index)}
              className={`mb-2 flex w-full items-center justify-between rounded-lg px-4 py-3 text-left text-sm ${step === index ? 'bg-primary text-white' : 'bg-muted text-foreground'}`}
              data-testid={`setup-step-${index}`}
            >
              <span>{label}</span>
              <span>{index + 1}</span>
            </button>
          ))}
        </aside>

        <section className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
          {step === 0 && (
            <div className="space-y-4" data-testid="setup-workspace">
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('home')}</span>
                <input value={home} onChange={(e) => setHome(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
              <div className="grid gap-4 md:grid-cols-2">
                <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg px-4 py-3 text-foreground">
                  <p className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{t('workingDirectory')}</p>
                  <p className="mt-2 break-all text-sm">{derivedWorkspace || '-'}</p>
                </div>
                <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg px-4 py-3 text-foreground">
                  <p className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{t('memoryPath')}</p>
                  <p className="mt-2 break-all text-sm">{derivedMemoryPath || '-'}</p>
                </div>
              </div>
              <p className="text-xs text-muted-foreground">{t('pathsDerivedFromHome')}</p>
              <div className="grid gap-4 md:grid-cols-2">
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('serverHost')}</span>
                  <input value={serverHost} onChange={(e) => setServerHost(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('serverPort')}</span>
                  <input value={serverPort} onChange={(e) => setServerPort(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
              </div>
            </div>
          )}

          {step === 1 && (
            <div className="space-y-4" data-testid="setup-provider">
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('providerName')}</span>
                <input value={providerName} onChange={(e) => setProviderName(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('providerBaseUrl')}</span>
                <input value={providerBaseUrl} onChange={(e) => setProviderBaseUrl(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                <p className="text-xs text-muted-foreground">{t('setupProviderHint')}</p>
              </label>
              <label className="space-y-2">
                <span className="text-sm font-medium text-foreground">{t('apiKey')}</span>
                <input value={apiKey} onChange={(e) => setApiKey(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" type="password" />
              </label>
              <div className="grid gap-4 md:grid-cols-2">
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('modelName')}</span>
                  <input value={modelName} onChange={(e) => setModelName(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
                <div className="rounded-lg border border-border bg-muted px-4 py-3 text-sm text-muted-foreground">
                  {t('setupEmbeddingOptionalHint')}
                </div>
              </div>
              <div className="rounded-lg border border-border bg-white px-4 py-3">
                <button
                  type="button"
                  onClick={() => setShowOptionalKnowledge((current) => !current)}
                  className="flex w-full items-center justify-between text-left"
                  data-testid="setup-toggle-optional-knowledge"
                >
                  <span className="text-sm font-medium text-foreground">{t('setupEmbeddingsOptionalTitle')}</span>
                  <span className="text-sm text-muted-foreground">{showOptionalKnowledge ? '−' : '+'}</span>
                </button>
                {showOptionalKnowledge && (
                  <div className="mt-4 space-y-4">
                    <label className="space-y-2">
                      <span className="text-sm font-medium text-foreground">{t('embeddingModel')}</span>
                      <input value={embeddingModel} onChange={(e) => setEmbeddingModel(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                    </label>
                    <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg px-4 py-3 text-foreground">
                      <p className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{t('ragDatabasePath')}</p>
                      <p className="mt-2 break-all text-sm">{derivedRagDb || '-'}</p>
                    </div>
                  </div>
                )}
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('maxConcurrency')}</span>
                  <input value={maxConcurrency} onChange={(e) => setMaxConcurrency(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('capabilityLevel')}</span>
                  <input value={capability} onChange={(e) => setCapability(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
              </div>
            </div>
          )}

          {step === 2 && (
            <div className="space-y-4" data-testid="setup-features">
              <div className="rounded-lg border border-border bg-muted px-4 py-3 text-sm text-muted-foreground">
                {t('pathsDerivedFromHome')}
              </div>
              <div className="grid gap-4 md:grid-cols-2">
                <label className="space-y-2">
                  <span className="text-sm font-medium text-foreground">{t('memoryStoreType')}</span>
                  <input value={memoryStoreType} onChange={(e) => setMemoryStoreType(e.target.value)} className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50" />
                </label>
                <div className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg px-4 py-3 text-foreground">
                  <p className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{t('memoryPath')}</p>
                  <p className="mt-2 break-all text-sm">{derivedMemoryPath || '-'}</p>
                </div>
              </div>
            </div>
          )}

          {step === 3 && (
            <div className="space-y-6" data-testid="setup-review">
              <div>
                <h3 className="text-lg font-semibold text-foreground">{t('setupReviewConfig')}</h3>
                <dl className="mt-4 grid gap-3 md:grid-cols-2">
                  {reviewItems.map(([label, value]) => (
                    <div key={label} className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                      <dt className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{label}</dt>
                      <dd className="mt-2 break-all text-sm text-foreground">{value}</dd>
                    </div>
                  ))}
                </dl>
              </div>
              <div>
                <h3 className="text-lg font-semibold text-foreground">{t('setupReviewProvider')}</h3>
                <dl className="mt-4 grid gap-3 md:grid-cols-2">
                  {providerItems.map(([label, value]) => (
                    <div key={label} className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4">
                      <dt className="text-xs uppercase tracking-[0.2em] text-muted-foreground">{label}</dt>
                      <dd className="mt-2 break-all text-sm text-foreground">{value}</dd>
                    </div>
                  ))}
                </dl>
              </div>
              <p className="rounded-lg border border-amber-200 bg-amber-50 px-4 py-3 text-sm text-amber-800">
                {t('setupRestartNotice')}
              </p>
              {saved && <p className="text-sm text-emerald-700">{t('setupCompleted')}</p>}
            </div>
          )}

          <div className="mt-8 flex items-center justify-between">
            <button
              type="button"
              onClick={() => setStep((current) => Math.max(0, current - 1))}
              className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2 text-sm"
              disabled={step === 0}
            >
              {t('setupBack')}
            </button>
            {step < STEP_COUNT - 1 ? (
              <button
                type="button"
                onClick={() => setStep((current) => Math.min(STEP_COUNT - 1, current + 1))}
                className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-5 py-2"
                data-testid="setup-next"
              >
                {t('setupNext')}
              </button>
            ) : (
              <button
                type="button"
                onClick={handleApply}
                disabled={applySetup.isPending}
                className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-5 py-2"
                data-testid="setup-apply"
              >
                {applySetup.isPending ? t('loading') : t('setupApply')}
              </button>
            )}
          </div>
        </section>
      </div>
    </div>
  )
}
