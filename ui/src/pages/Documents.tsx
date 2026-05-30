import { useState, useRef } from 'react'
import { useTranslation } from 'react-i18next'
import { useDocuments, useCollections, useDeleteDocument, useDocument } from '../hooks/useApi'
import { api } from '../lib/api'

function getDocumentPath(doc: { path?: string; metadata?: Record<string, unknown> }) {
  if (doc.path) return doc.path
  return typeof doc.metadata?.file_path === 'string' ? doc.metadata.file_path : ''
}

function getDocumentFilename(doc: { path?: string; metadata?: Record<string, unknown> }) {
  const path = getDocumentPath(doc)
  return path.split('/').pop() || path || ''
}

function getDocumentCreated(doc: { created?: string; metadata?: Record<string, unknown> }) {
  if (doc.created) return doc.created
  return typeof doc.metadata?.creation_date === 'string' ? doc.metadata.creation_date : ''
}

function getDocumentExtension(doc: { metadata?: Record<string, unknown> }) {
  return typeof doc.metadata?.file_ext === 'string' ? doc.metadata.file_ext : ''
}

function formatDocumentDate(value: string) {
  if (!value) return '-'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function DocumentDetailModal({ docId, onClose }: { docId: string; onClose: () => void }) {
  const { t } = useTranslation()
  const { data: doc, isLoading, error } = useDocument(docId)

  if (isLoading) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/10 backdrop-blur-sm">
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-6">
          <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
        </div>
      </div>
    )
  }

  if (error || !doc) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/10 backdrop-blur-sm" onClick={onClose}>
        <div className="rounded-lg border bg-card text-card-foreground shadow-sm max-w-lg rounded-lg p-6">
          <p className="text-rose-700">{t('errorLoadingDocument')}</p>
          <button onClick={onClose} className="inline-flex items-center justify-center rounded-md border border-input bg-background text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground disabled:pointer-events-none disabled:opacity-50 mt-4 px-4 py-2">
            {t('closeButton')}
          </button>
        </div>
      </div>
    )
  }

  const filename = getDocumentFilename(doc)
  const path = getDocumentPath(doc)
  const created = getDocumentCreated(doc)
  const extension = getDocumentExtension(doc)
  const metadataEntries = Object.entries(doc.metadata ?? {})

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-foreground/10 backdrop-blur-sm" onClick={onClose}>
      <div className="rounded-lg border bg-card text-card-foreground shadow-sm max-h-[80vh] max-w-2xl overflow-auto rounded-lg p-6" onClick={e => e.stopPropagation()} data-testid="document-detail-modal">
        <div className="flex justify-between items-start mb-4">
          <h3 className="text-lg font-semibold text-foreground">{filename}</h3>
          <button onClick={onClose} className="text-muted-foreground hover:text-foreground" data-testid="document-detail-close">
            <svg className="w-5 h-5" fill="none" stroke="currentColor" viewBox="0 0 24 24">
              <path strokeLinecap="round" strokeLinejoin="round" strokeWidth={2} d="M6 18L18 6M6 6l12 12" />
            </svg>
          </button>
        </div>
        <div className="space-y-3 text-sm">
          <div>
            <span className="text-muted-foreground">{t('id')}: </span>
            <span className="font-mono text-foreground">{doc.id}</span>
          </div>
          <div>
            <span className="text-muted-foreground">{t('path')}: </span>
            <span className="break-all text-foreground">{path || '-'}</span>
          </div>
          <div>
            <span className="text-muted-foreground">{t('created')}: </span>
            <span className="text-foreground">{formatDocumentDate(created)}</span>
          </div>
          <div>
            <span className="text-muted-foreground">{t('type')}: </span>
            <span className="text-foreground">{extension || '-'}</span>
          </div>
          {metadataEntries.length > 0 && (
            <div>
              <span className="text-muted-foreground">{t('metadata')}: </span>
              <div className="mt-2 rounded-xl border border-border bg-muted/60 p-3">
                <dl className="space-y-2">
                  {metadataEntries.map(([key, value]) => (
                    <div key={key} className="grid grid-cols-[140px_1fr] gap-3">
                      <dt className="font-medium text-muted-foreground">{key}</dt>
                      <dd className="break-all text-foreground">{String(value)}</dd>
                    </div>
                  ))}
                </dl>
              </div>
            </div>
          )}
          {doc.content && (
            <div>
              <span className="text-muted-foreground">{t('content')}: </span>
              <pre className="mt-2 max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-xl border border-border bg-muted/60 p-3 text-xs text-foreground">{doc.content}</pre>
            </div>
          )}
        </div>
      </div>
    </div>
  )
}

export function Documents() {
  const { t } = useTranslation()
  const { data: documents, isLoading: docsLoading, error: docsError } = useDocuments()
  const { data: collections } = useCollections()
  const deleteDoc = useDeleteDocument()
  const [selectedDocId, setSelectedDocId] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const handleUpload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0]
    if (!file) return

    setUploading(true)
    try {
      const formData = new FormData()
      formData.append('file', file)
      const result = await api.ingest(formData)
      if (result.error) {
        alert(result.error)
      } else {
        window.location.reload()
      }
    } catch (err) {
      alert(t('uploadFailed', { message: err instanceof Error ? err.message : t('unknownError') }))
    } finally {
      setUploading(false)
      if (fileInputRef.current) {
        fileInputRef.current.value = ''
      }
    }
  }

  const handleDelete = async (id: string, path: string) => {
    const filename = path?.split('/').pop() || path || t('thisDocument')
    if (!confirm(t('deleteDocumentPrompt', { name: filename }))) return
    try {
      await deleteDoc.mutateAsync(id)
    } catch (err) {
      alert(t('deleteFailed', { message: err instanceof Error ? err.message : t('unknownError') }))
    }
  }

  if (docsLoading) {
    return (
      <div className="flex items-center justify-center h-64">
        <div className="animate-spin rounded-full h-8 w-8 border-b-2 border-primary"></div>
      </div>
    )
  }

  if (docsError) {
    return (
      <div className="rounded-lg border border-rose-200 bg-rose-50 p-4">
        <p className="text-rose-700">
          {t('error')}: {docsError.message}
        </p>
      </div>
    )
  }

  return (
    <div className="space-y-6" data-testid="page-documents">
      <div className="flex items-center justify-between">
        <h2 className="text-xl font-semibold text-foreground">
          {t('documentsCollections')}
        </h2>
        <div>
          <input
            ref={fileInputRef}
            type="file"
            onChange={handleUpload}
            className="hidden"
            accept=".txt,.md,.pdf,.json,.csv"
            data-testid="documents-file-input"
          />
          <button
            onClick={() => fileInputRef.current?.click()}
            disabled={uploading}
            className="inline-flex items-center justify-center gap-2 rounded-md text-sm font-medium transition-colors bg-primary text-primary-foreground shadow hover:bg-primary/90 disabled:pointer-events-none disabled:opacity-50 px-4 py-2"
            data-testid="documents-upload"
          >
            {uploading ? t('uploading') : t('uploadFile')}
          </button>
        </div>
      </div>

      {collections && collections.length > 0 && (
        <div>
          <h3 className="mb-3 text-lg font-medium text-foreground">
            {t('collections')}
          </h3>
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            {collections.map((collection) => (
              <div
                key={collection.name}
                className="rounded-lg border bg-card text-card-foreground shadow-sm rounded-lg p-4"
              >
                <h4 className="font-medium text-foreground">
                  {collection.name}
                </h4>
                <p className="text-sm text-muted-foreground">
                  {t('documentsCount')}: {collection.count}
                </p>
              </div>
            ))}
          </div>
        </div>
      )}

      {documents && documents.length > 0 && (
        <div>
          <h3 className="mb-3 text-lg font-medium text-foreground">
            {t('documentsTotal', { count: documents.length })}
          </h3>
          <div className="overflow-x-auto rounded-lg border border-border bg-white" data-testid="documents-table">
            <table className="min-w-full divide-y divide-border">
              <thead className="bg-muted/70">
                <tr>
                  <th className="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {t('filename')}
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {t('path')}
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {t('type')}
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {t('created')}
                  </th>
                  <th className="px-6 py-3 text-left text-xs font-medium uppercase tracking-wider text-muted-foreground">
                    {t('actions')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border bg-white">
                {documents.map((doc) => {
                  const filename = getDocumentFilename(doc)
                  const path = getDocumentPath(doc)
                  const extension = getDocumentExtension(doc)
                  const created = getDocumentCreated(doc)
                  return (
                    <tr key={doc.id} className="hover:bg-muted/40" data-testid={`document-row-${doc.id}`}>
                      <td className="whitespace-nowrap px-6 py-4 text-sm font-medium text-foreground">
                        {filename}
                      </td>
                      <td className="max-w-md break-all px-6 py-4 text-sm text-muted-foreground">
                        {path || '-'}
                      </td>
                      <td className="whitespace-nowrap px-6 py-4 text-sm text-muted-foreground">
                        {extension || '-'}
                      </td>
                      <td className="whitespace-nowrap px-6 py-4 text-sm text-muted-foreground">
                        {formatDocumentDate(created)}
                      </td>
                      <td className="px-6 py-4 whitespace-nowrap text-sm space-x-2">
                        <button
                          onClick={() => setSelectedDocId(doc.id)}
                          className="text-foreground hover:text-foreground"
                          data-testid={`document-view-${doc.id}`}
                        >
                          {t('viewDocument')}
                        </button>
                        <button
                          onClick={() => handleDelete(doc.id, path)}
                          className="text-red-600 hover:text-red-800"
                          data-testid={`document-delete-${doc.id}`}
                        >
                          {t('delete')}
                        </button>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        </div>
      )}

      {(!documents || documents.length === 0) && (
        <div className="rounded-lg border border-dashed border-border bg-muted/60 py-12 text-center">
          <p className="text-muted-foreground">
            {t('noDocumentsFound')}
          </p>
        </div>
      )}

      {selectedDocId && (
        <DocumentDetailModal docId={selectedDocId} onClose={() => setSelectedDocId(null)} />
      )}
    </div>
  )
}
