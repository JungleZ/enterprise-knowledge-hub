import { useEffect, useState } from 'react'
import { useParams, useSearchParams, useNavigate } from 'react-router-dom'
import { FileText, ArrowLeft, ChevronDown, ChevronUp } from 'lucide-react'
import { api, type DocContent } from '../api/client'
import { cn } from '../lib/utils'

export default function DocumentViewPage() {
  const { docId } = useParams()
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const targetChunk = Number(params.get('chunk') ?? '0')
  const [doc, setDoc] = useState<DocContent | null>(null)
  const [error, setError] = useState('')
  const [open, setOpen] = useState<string | null>(null)

  useEffect(() => {
    if (!docId) return
    api.docs
      .chunks(docId)
      .then((d) => {
        setDoc(d)
        const t = d.chunks.find((c) => c.chunk_index === targetChunk)
        if (t) setOpen(t.id)
      })
      .catch((e) => setError(e.message || '加载失败'))
  }, [docId])

  const toggle = (id: string) => setOpen((cur) => (cur === id ? null : id))

  return (
    <div className="h-full overflow-y-auto p-4 md:p-6 bg-gray-50">
      <div className="max-w-3xl mx-auto">
        <button
          onClick={() => navigate(-1)}
          className="inline-flex items-center gap-1 text-sm text-gray-500 hover:text-gray-800 mb-3 transition-colors"
        >
          <ArrowLeft size={16} />
          返回
        </button>

        {error ? (
          <div className="card text-center text-red-500 text-sm py-10">{error}</div>
        ) : !doc ? (
          <div className="card text-center text-gray-400 text-sm py-10">加载中…</div>
        ) : (
          <>
            <div className="flex items-center gap-2.5 mb-4">
              <div className="w-9 h-9 rounded-lg bg-brand-100 text-brand-600 flex items-center justify-center shrink-0">
                <FileText size={18} />
              </div>
              <div className="min-w-0">
                <h2 className="text-lg font-semibold truncate">{doc.doc_title}</h2>
                <p className="text-xs text-gray-400">共 {doc.chunk_count} 段</p>
              </div>
            </div>

            <div className="space-y-2">
              {doc.chunks.map((ch) => {
                const isTarget = ch.chunk_index === targetChunk
                const isOpen = open === ch.id
                return (
                  <div
                    key={ch.id}
                    className={cn(
                      'card overflow-hidden transition-colors',
                      isTarget && !isOpen ? 'ring-2 ring-amber-400' : '',
                    )}
                  >
                    <button
                      onClick={() => toggle(ch.id)}
                      className="w-full flex items-center justify-between gap-2 px-3.5 py-2.5 hover:bg-gray-50 transition-colors"
                    >
                      <span className="text-xs text-gray-500">第 {ch.chunk_index + 1} 段</span>
                      {isOpen ? (
                        <ChevronUp size={14} className="text-gray-400" />
                      ) : (
                        <ChevronDown size={14} className="text-gray-400" />
                      )}
                    </button>
                    {isOpen && (
                      <div className="px-4 pb-4 text-sm text-gray-700 whitespace-pre-wrap leading-relaxed">
                        {ch.text}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </>
        )}
      </div>
    </div>
  )
}