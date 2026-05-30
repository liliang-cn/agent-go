import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useMemories, useAddMemory, useDeleteMemory } from '../hooks/useApi'
import type { Memory, AddMemoryRequest } from '../lib/api'

export function Memory() {
  const { t } = useTranslation()
  const [showAddForm, setShowAddForm] = useState(false)
  const [searchQuery, setSearchQuery] = useState('')
  const [selectedMemory, setSelectedMemory] = useState<Memory | null>(null)
  
  const { data: memories, isLoading, error, refetch } = useMemories()
  const addMutation = useAddMemory()
  const deleteMutation = useDeleteMemory()
  
  // Filter memories based on search
  const filteredMemories = searchQuery 
    ? memories?.filter(m => 
        m.content.toLowerCase().includes(searchQuery.toLowerCase()) ||
        m.type.toLowerCase().includes(searchQuery.toLowerCase())
      )
    : memories

  const handleAddMemory = async (e: React.FormEvent<HTMLFormElement>) => {
    e.preventDefault()
    const formData = new FormData(e.currentTarget)
    const data: AddMemoryRequest = {
      content: formData.get('content') as string,
      type: formData.get('type') as string || 'fact',
      importance: parseFloat(formData.get('importance') as string) || 0.5,
    }
    await addMutation.mutateAsync(data)
    setShowAddForm(false)
  }

  const handleDeleteMemory = async (id: string) => {
    if (confirm(t('confirmDeleteMemory'))) {
      await deleteMutation.mutateAsync(id)
      if (selectedMemory?.id === id) {
        setSelectedMemory(null)
      }
    }
  }

  const getTypeColor = (type: string) => {
    switch (type) {
      case 'fact': return 'bg-muted text-foreground'
      case 'skill': return 'bg-muted text-foreground'
      case 'pattern': return 'bg-emerald-100 text-emerald-700'
      case 'context': return 'bg-amber-100 text-amber-700'
      case 'preference': return 'bg-pink-100 text-pink-700'
      default: return 'bg-muted text-foreground'
    }
  }

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
      </div>
    )
  }

  if (error) {
    return (
      <div className="rounded-lg border border-rose-200 bg-rose-50 p-4">
        <p className="text-rose-700">{t('errorLoadingMemories')}: {error.message}</p>
        <button onClick={() => refetch()} className="mt-2 px-4 py-2 bg-red-600 text-white rounded-lg">
          {t('retry')}
        </button>
      </div>
    )
  }

  return (
    <div className="space-y-6" data-testid="page-memory">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">{t('memoryNav')}</h2>
        <div className="flex gap-2">
          <button
            onClick={() => refetch()}
            className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 px-4 py-2 text-sm"
            data-testid="memory-refresh"
          >
            {t('refresh')}
          </button>
          <button
            onClick={() => setShowAddForm(true)}
            className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-4 py-2 text-sm"
            data-testid="memory-add"
          >
            {t('addMemory')}
          </button>
        </div>
      </div>

      {/* Search Bar */}
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-4">
        <input
          type="text"
          value={searchQuery}
          onChange={(e) => setSearchQuery(e.target.value)}
          placeholder={t('searchMemories')}
          className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          data-testid="memory-search"
        />
      </div>

      {/* Add Memory Form */}
      {showAddForm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/10 backdrop-blur-sm">
          <div className="rounded-lg border bg-card text-card-foreground shadow-sm mx-4 w-full max-w-lg rounded-lg p-6" data-testid="memory-add-modal">
            <h3 className="mb-4 text-lg font-semibold text-foreground">{t('addNewMemory')}</h3>
            <form onSubmit={handleAddMemory} className="space-y-4" data-testid="memory-add-form">
              <div>
                <label className="mb-2 block text-sm font-medium text-foreground">
                  {t('typeLabel')}
                </label>
                <select
                  name="type"
                  className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                >
                  <option value="fact">{t('fact')}</option>
                  <option value="skill">{t('skill')}</option>
                  <option value="pattern">{t('pattern')}</option>
                  <option value="context">{t('context')}</option>
                  <option value="preference">{t('preference')}</option>
                </select>
              </div>
              <div>
                <label className="mb-2 block text-sm font-medium text-foreground">
                  {t('contentLabel')} *
                </label>
                <textarea
                  name="content"
                  required
                  rows={4}
                  className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50 resize-none"
                  placeholder={t('enterMemoryContent')}
                />
              </div>
              <div>
                <label className="mb-2 block text-sm font-medium text-foreground">
                  {t('importanceLabel')} (0-1)
                </label>
                <input
                  type="number"
                  name="importance"
                  step="0.1"
                  min="0"
                  max="1"
                  defaultValue={0.5}
                  className="flex w-full rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-sm transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                />
              </div>
              <div className="flex gap-2">
                <button
                  type="submit"
                  disabled={addMutation.isPending}
                  className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-6 py-2 disabled:opacity-50"
                  data-testid="memory-add-submit"
                >
                  {addMutation.isPending ? t('adding') : t('addButton')}
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
        </div>
      )}

      {/* Memory Detail Panel */}
      {selectedMemory && (
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6" data-testid="memory-detail-panel">
          <div className="flex items-center justify-between mb-4">
            <h3 className="text-lg font-medium text-foreground">{t('memoryDetails')}</h3>
            <button
              onClick={() => setSelectedMemory(null)}
              className="text-muted-foreground hover:text-foreground"
            >
              {t('closeButton')}
            </button>
          </div>
          <div className="space-y-4">
            <div>
              <span className="text-sm text-muted-foreground">{t('id')}:</span>
              <p className="font-mono text-sm text-foreground">{selectedMemory.id}</p>
            </div>
            <div>
              <span className="text-sm text-muted-foreground">{t('typeLabel')}:</span>
              <p className={`inline-block px-2 py-1 text-xs rounded ${getTypeColor(selectedMemory.type)}`}>
                {selectedMemory.type}
              </p>
            </div>
            <div>
              <span className="text-sm text-muted-foreground">{t('contentLabel')}:</span>
              <p className="text-foreground">{selectedMemory.content}</p>
            </div>
            <div className="flex gap-4 text-sm text-muted-foreground">
              <span>{t('importanceLabel')}: {selectedMemory.importance.toFixed(2)}</span>
              <span>{t('created')}: {new Date(selectedMemory.created_at).toLocaleString()}</span>
            </div>
            <button
              onClick={() => handleDeleteMemory(selectedMemory.id)}
              className="px-4 py-2 text-sm bg-red-600 text-white rounded-lg hover:bg-red-700"
              data-testid="memory-delete"
            >
              {t('deleteMemory')}
            </button>
          </div>
        </div>
      )}

      {/* Memories List */}
      <div className="grid grid-cols-1 md:grid-cols-2 lg:grid-cols-3 gap-4" data-testid="memory-list">
        {filteredMemories && filteredMemories.length > 0 ? (
          filteredMemories.map((memory) => (
            <div
              key={memory.id}
              className="rounded-lg border border-border bg-white p-4 transition-colors hover:border-border hover:bg-muted/40 cursor-pointer"
              onClick={() => setSelectedMemory(memory)}
              data-testid={`memory-card-${memory.id}`}
            >
              <div className="flex items-start justify-between mb-2">
                <span className={`px-2 py-1 text-xs rounded ${getTypeColor(memory.type)}`}>
                  {memory.type}
                </span>
                <span className="text-xs text-muted-foreground">
                  {memory.importance.toFixed(2)}
                </span>
              </div>
              <p className="line-clamp-3 text-sm text-foreground">
                {memory.content}
              </p>
              <p className="mt-2 text-xs text-muted-foreground">
                {new Date(memory.created_at).toLocaleDateString()}
              </p>
            </div>
          ))
        ) : (
          <div className="col-span-full rounded-lg border border-dashed border-border bg-muted/60 py-12 text-center text-muted-foreground">
            {searchQuery ? t('noMemoriesMatch') : t('noMemoriesFoundCta')}
          </div>
        )}
      </div>
    </div>
  )
}
