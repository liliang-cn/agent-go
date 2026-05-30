import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useSkills, useCreateSkill, useDeleteSkill } from '../hooks/useApi'
import type { Skill, CreateSkillRequest } from '../lib/api'

function formatTimestamp(value?: string) {
  if (!value) return '-'
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  return date.toLocaleString()
}

function displayValue(value?: string | number | boolean | null) {
  if (value === undefined || value === null || value === '') return '-'
  return String(value)
}

export function Skills() {
  const { t } = useTranslation()
  const [showAddForm, setShowAddForm] = useState(false)
  const [selectedSkill, setSelectedSkill] = useState<Skill | null>(null)
  const { data: skills, isLoading, error, refetch } = useSkills()
  const createMutation = useCreateSkill()
  const deleteMutation = useDeleteSkill()

  const handleCreateSkill = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const formData = new FormData(e.currentTarget)
    const data: CreateSkillRequest = {
      name: formData.get('name') as string,
      description: formData.get('description') as string,
      content: formData.get('content') as string,
    }
    await createMutation.mutateAsync(data)
    setShowAddForm(false)
  }

  const handleDeleteSkill = async (id: string) => {
    if (confirm(t('confirmDeleteSkill'))) {
      await deleteMutation.mutateAsync(id)
      if (selectedSkill?.id === id) {
        setSelectedSkill(null)
      }
    }
  }

  if (isLoading) {
    return (
      <div className="flex h-64 items-center justify-center">
        <div className="h-8 w-8 animate-spin rounded-full border-b-2 border-primary"></div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="rounded-lg border border-rose-200 bg-rose-50 p-4">
        <p className="text-rose-700">{t('errorLoadingSkills')}: {error.message}</p>
        <button onClick={() => refetch()} className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 mt-2 px-4 py-2">
          {t('retry')}
        </button>
      </div>
    )
  }

  return (
    <div className="space-y-6" data-testid="page-skills">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">{t('skills')}</h2>
        <button
          onClick={() => setShowAddForm(!showAddForm)}
          className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-4 py-2"
          data-testid="skills-toggle-create"
        >
          {showAddForm ? t('cancel') : t('addSkillButton')}
        </button>
      </div>

      {showAddForm && (
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6" data-testid="skills-create-panel">
          <h3 className="mb-4 text-lg font-medium text-foreground">{t('createNewSkill')}</h3>
          <form onSubmit={handleCreateSkill} className="space-y-4" data-testid="skills-create-form">
            <div>
              <label className="mb-2 block text-sm font-medium text-foreground">
                {t('skillNameRequired')}
              </label>
              <input
                type="text"
                name="name"
                required
                className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                placeholder={t('skillNameExample')}
              />
            </div>
            <div>
              <label className="mb-2 block text-sm font-medium text-foreground">
                {t('skillDescriptionLabel')}
              </label>
              <input
                type="text"
                name="description"
                className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                placeholder={t('skillDescriptionPlaceholder')}
              />
            </div>
            <div>
              <label className="mb-2 block text-sm font-medium text-foreground">
                {t('skillContentLabel')}
              </label>
              <textarea
                name="content"
                required
                rows={10}
                className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 font-mono text-sm"
                placeholder={t('skillContentPlaceholder')}
              />
            </div>
            <div className="flex gap-2">
              <button
                type="submit"
                disabled={createMutation.isPending}
                className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-6 py-2"
                data-testid="skills-create-submit"
              >
                {createMutation.isPending ? t('creating') : t('createButton')}
              </button>
              <button
                type="button"
                onClick={() => setShowAddForm(false)}
                className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-6 py-2"
              >
                {t('cancel')}
              </button>
            </div>
          </form>
        </div>
      )}

      <div className="space-y-4" data-testid="skills-list">
        {skills && skills.length > 0 ? (
          skills.map((skill) => (
            <section
              key={skill.id}
              data-testid={`skill-card-${skill.id}`}
              className="rounded-lg border bg-muted/40 text-card-foreground shadow-sm rounded-lg p-4"
            >
              <div className="flex items-start justify-between gap-4">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-3">
                    <h3 className="font-medium text-foreground">{skill.name}</h3>
                    <span className={`rounded px-2 py-1 text-xs ${skill.enabled ? 'bg-emerald-100 text-emerald-700' : 'bg-muted text-foreground'}`}>
                      {skill.enabled ? t('skillEnabled') : t('disabled')}
                    </span>
                  </div>
                  <p className="mt-2 text-sm text-muted-foreground">
                    {skill.description || t('noDescription')}
                  </p>
                  <div className="mt-2 flex flex-wrap gap-3 text-xs text-muted-foreground">
                    <span>{t('version')}: {displayValue(skill.version)}</span>
                    <span>{t('category')}: {displayValue(skill.category)}</span>
                    <span>{t('author')}: {displayValue(skill.author)}</span>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-3 text-sm">
                  <button
                    type="button"
                    onClick={() => setSelectedSkill(skill)}
                    className="text-foreground hover:text-foreground"
                    data-testid={`skill-open-${skill.id}`}
                  >
                    {t('viewDetails')}
                  </button>
                  <button
                    type="button"
                    onClick={() => handleDeleteSkill(skill.id)}
                    className="text-red-600 hover:text-red-700"
                  >
                    {t('delete')}
                  </button>
                </div>
              </div>
            </section>
          ))
        ) : (
          <div className="py-12 text-center text-muted-foreground">
            {t('noSkillsFound')}
          </div>
        )}
      </div>

      {selectedSkill && (
        <div
          className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/10 px-4 backdrop-blur-sm"
          onClick={() => setSelectedSkill(null)}
          data-testid="skills-detail-modal-overlay"
        >
          <div
            className="rounded-lg border bg-card text-card-foreground shadow-sm max-h-[85vh] w-full max-w-4xl overflow-auto rounded-lg p-6"
            onClick={(event) => event.stopPropagation()}
            data-testid="skills-detail-modal"
          >
            <div className="flex items-start justify-between gap-4">
              <div>
                <h3 className="text-xl font-semibold text-foreground">{selectedSkill.name}</h3>
                <p className="mt-2 max-w-3xl text-sm text-muted-foreground">
                  {selectedSkill.description || t('noDescription')}
                </p>
              </div>
              <button
                type="button"
                onClick={() => setSelectedSkill(null)}
                className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2"
              >
                {t('closeButton')}
              </button>
            </div>

            <div className="mt-6 grid gap-3 md:grid-cols-2 xl:grid-cols-4">
              <div className="rounded-[18px] border border-border bg-white p-3">
                <p className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t('author')}</p>
                <p className="mt-2 text-sm font-medium text-foreground">{displayValue(selectedSkill.author)}</p>
              </div>
              <div className="rounded-[18px] border border-border bg-white p-3">
                <p className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t('category')}</p>
                <p className="mt-2 text-sm font-medium text-foreground">{displayValue(selectedSkill.category)}</p>
              </div>
              <div className="rounded-[18px] border border-border bg-white p-3">
                <p className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t('version')}</p>
                <p className="mt-2 text-sm font-medium text-foreground">{displayValue(selectedSkill.version)}</p>
              </div>
              <div className="rounded-[18px] border border-border bg-white p-3">
                <p className="text-xs uppercase tracking-[0.18em] text-muted-foreground">{t('enabled')}</p>
                <p className="mt-2 text-sm font-medium text-foreground">{selectedSkill.enabled ? t('yes') : t('no')}</p>
              </div>
            </div>

            <div className="mt-4 rounded-[18px] border border-border bg-white p-4">
              <div className="grid gap-3 md:grid-cols-2">
                <p className="text-sm text-muted-foreground">
                  <span className="font-medium text-foreground">{t('path')}:</span> {displayValue(selectedSkill.path)}
                </p>
                <p className="text-sm text-muted-foreground">
                  <span className="font-medium text-foreground">{t('created')}:</span> {formatTimestamp(selectedSkill.created_at || selectedSkill.created)}
                </p>
              </div>
              <div className="mt-4">
                <h4 className="text-sm font-medium text-foreground">{t('tags')}</h4>
                <div className="mt-2 flex flex-wrap gap-2">
                  {selectedSkill.tags && selectedSkill.tags.length > 0 ? (
                    selectedSkill.tags.map((tag) => (
                      <span key={tag} className="rounded-full bg-muted px-2 py-1 text-xs font-medium text-foreground">
                        {tag}
                      </span>
                    ))
                  ) : (
                    <span className="text-sm text-muted-foreground">-</span>
                  )}
                </div>
              </div>
            </div>

            {selectedSkill.variables && Object.keys(selectedSkill.variables).length > 0 && (
              <div className="mt-4">
                <h4 className="mb-2 text-sm font-medium text-foreground">{t('variables')}</h4>
                <div className="space-y-2">
                  {Object.entries(selectedSkill.variables).map(([name, def]) => (
                    <div key={name} className="rounded-[18px] border border-border bg-white p-3">
                      <p className="font-medium text-foreground">{name}</p>
                      <p className="text-sm text-muted-foreground">{def.description || t('noDescription')}</p>
                      <p className="text-xs text-muted-foreground">
                        {t('type')}: {def.type} | {t('required')}: {def.required ? t('yes') : t('no')}
                      </p>
                    </div>
                  ))}
                </div>
              </div>
            )}

            {selectedSkill.steps && selectedSkill.steps.length > 0 && (
              <div className="mt-4">
                <h4 className="mb-2 text-sm font-medium text-foreground">{t('steps')}</h4>
                <div className="space-y-2">
                  {selectedSkill.steps.map((step, index) => (
                    <div key={step.id || `${selectedSkill.id}-step-${index}`} className="rounded-[18px] border border-border bg-white p-3">
                      <p className="font-medium text-foreground">
                        {index + 1}. {step.title || t('untitledStep')}
                      </p>
                      <p className="mt-1 text-sm text-muted-foreground">{step.description || t('noDescription')}</p>
                      {step.content && (
                        <pre className="mt-2 overflow-x-auto rounded-[14px] bg-muted p-3 text-xs text-foreground">
                          <code>{step.content}</code>
                        </pre>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}
