import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { Plus, Upload, Trash2, RefreshCw, FileText, Database, Eye } from 'lucide-react'
import { api, type KnowledgeBase, type Document } from '../api/client'
import { cn, formatDate, isAdminRole } from '../lib/utils'
import { useAuthStore } from '../stores/auth'

export default function KBPage() {
  const { user } = useAuthStore()
  const navigate = useNavigate()
  const isAdmin = isAdminRole(user?.role)
  const [kbs, setKbs] = useState<KnowledgeBase[]>([])
  const [selected, setSelected] = useState<KnowledgeBase | null>(null)
  const [docs, setDocs] = useState<Document[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [showKbPicker, setShowKbPicker] = useState(false)
  const [newName, setNewName] = useState('')
  const [newDesc, setNewDesc] = useState('')
  const [newDept, setNewDept] = useState('')
  const [uploading, setUploading] = useState(false)

  const loadKbs = async () => {
    const list = await api.kbs.list()
    setKbs(list)
    if (selected) {
      const cur = list.find((k) => k.id === selected.id)
      setSelected(cur || null)
    }
  }

  useEffect(() => {
    loadKbs().catch(() => {})
  }, [])

  const selectKb = async (kb: KnowledgeBase) => {
    setSelected(kb)
    setShowKbPicker(false)
    const list = await api.docs.list(kb.id)
    setDocs(list)
  }

  const createKb = async () => {
    if (!newName.trim()) return
    await api.kbs.create({ name: newName.trim(), description: newDesc, allowed_departments: newDept })
    setNewName('')
    setNewDesc('')
    setNewDept('')
    setShowCreate(false)
    loadKbs()
  }

  const upload = async (e: React.ChangeEvent<HTMLInputElement>) => {
    if (!selected || !e.target.files?.length) return
    setUploading(true)
    try {
      for (const file of Array.from(e.target.files)) {
        const title = file.name.replace(/\.[^.]+$/, '')
        await api.docs.upload(selected.id, file, title, '')
      }
      await waitReady(selected.id)
    } catch (err: any) {
      alert(err.message || '上传失败')
    } finally {
      setUploading(false)
      e.target.value = ''
    }
  }

  // Poll until every doc in this KB leaves the 'processing' state (max ~30s).
  const waitReady = async (kbId: string) => {
    const deadline = Date.now() + 30000
    while (Date.now() < deadline) {
      const list = await api.docs.list(kbId)
      setDocs(list)
      if (!list.some((d) => d.status === 'processing')) return
      await new Promise((r) => setTimeout(r, 1000))
    }
  }

  const deleteDoc = async (doc: Document) => {
    if (!confirm(`确认删除文档「${doc.title}」？`)) return
    await api.docs.remove(doc.id)
    setDocs((prev) => prev.filter((d) => d.id !== doc.id))
  }

  const reprocess = async (doc: Document) => {
    if (!selected) return
    setDocs((prev) => prev.map((d) => (d.id === doc.id ? { ...d, status: 'processing' } : d)))
    try {
      await api.docs.reprocess(doc.id)
      await waitReady(selected.id)
    } catch (err: any) {
      alert(err.message || '重新处理失败')
    }
  }

  const deleteKb = async (kb: KnowledgeBase) => {
    if (!confirm(`确认删除知识库「${kb.name}」及其全部文档？`)) return
    await api.kbs.remove(kb.id)
    if (selected?.id === kb.id) {
      setSelected(null)
      setDocs([])
    }
    loadKbs()
  }

  const kbListBody = (
    <>
      {showCreate && (
        <div className="p-3 border-b border-gray-100 space-y-2">
          <input className="input text-sm" placeholder="知识库名称" value={newName} onChange={(e) => setNewName(e.target.value)} />
          <input className="input text-sm" placeholder="描述" value={newDesc} onChange={(e) => setNewDesc(e.target.value)} />
          <input className="input text-sm" placeholder="可见部门（逗号分隔，留空=全员）" value={newDept} onChange={(e) => setNewDept(e.target.value)} />
          <div className="flex gap-2">
            <button onClick={createKb} className="btn-primary !py-1.5 text-xs flex-1">创建</button>
            <button onClick={() => setShowCreate(false)} className="btn-ghost !py-1.5 text-xs">取消</button>
          </div>
        </div>
      )}
      {kbs.map((kb) => (
        <div
          key={kb.id}
          onClick={() => selectKb(kb)}
          className={cn(
            'group flex items-start justify-between px-3 py-2.5 mx-2 mt-1 rounded-lg cursor-pointer border',
            selected?.id === kb.id
              ? 'bg-brand-50 border-brand-200'
              : 'border-transparent hover:bg-gray-50',
          )}
        >
          <div className="min-w-0">
            <div className="flex items-center gap-1.5 text-sm font-medium text-gray-800">
              <Database size={14} className="text-brand-500 shrink-0" />
              <span className="truncate">{kb.name}</span>
            </div>
            <div className="text-xs text-gray-400 mt-0.5 truncate">
              {kb.description || '暂无描述'}
            </div>
          </div>
          {isAdmin && (
            <button
              onClick={(e) => {
                e.stopPropagation()
                deleteKb(kb)
              }}
              className="opacity-0 group-hover:opacity-100 text-gray-300 hover:text-red-500 shrink-0 ml-2 transition-opacity"
              title="删除知识库"
            >
              <Trash2 size={14} />
            </button>
          )}
        </div>
      ))}
      {kbs.length === 0 && <div className="text-center text-gray-400 text-xs mt-8">暂无知识库</div>}
    </>
  )

  return (
    <div className="h-full flex relative">
      {/* KB list (desktop) */}
      <div className="hidden md:flex w-72 shrink-0 border-r border-gray-200 bg-white overflow-y-auto flex-col">
        <div className="p-3 border-b border-gray-100 flex items-center justify-between shrink-0">
          <span className="text-sm font-medium text-gray-700">知识库</span>
          {isAdmin && (
            <button onClick={() => setShowCreate(true)} className="btn-ghost !px-2 !py-1 text-xs">
              <Plus size={14} />
              新建
            </button>
          )}
        </div>
        {kbListBody}
      </div>

      {/* mobile KB picker drawer */}
      {showKbPicker && (
        <div className="md:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/40" onClick={() => setShowKbPicker(false)} />
          <div className="absolute left-0 bottom-0 right-0 bg-white rounded-t-2xl max-h-[70vh] overflow-y-auto p-3">
            <div className="flex items-center justify-between mb-2">
              <span className="text-sm font-medium text-gray-700">选择知识库</span>
              <button
                onClick={() => setShowKbPicker(false)}
                className="text-xs text-gray-400 hover:text-gray-600 bg-gray-100 rounded-md px-2 py-1"
              >
                收起
              </button>
            </div>
            <div className="space-y-1">{kbListBody}</div>
          </div>
        </div>
      )}

      {/* docs list */}
      <div className="flex-1 overflow-y-auto p-4 md:p-6 min-w-0">
        {!selected ? (
          <div className="h-full flex flex-col items-center justify-center text-gray-400 gap-3">
            <div className="hidden md:block">选择左侧知识库查看文档</div>
            <button
              onClick={() => setShowKbPicker(true)}
              className="md:hidden btn-primary"
            >
              <Database size={16} />
              选择知识库
            </button>
          </div>
        ) : (
          <div className="max-w-4xl mx-auto">
            <div className="flex flex-col sm:flex-row sm:items-center justify-between gap-2 mb-4">
              <div className="min-w-0">
                <h2 className="text-lg font-semibold truncate">{selected.name}</h2>
                <p className="text-sm text-gray-500 mt-0.5 truncate">{selected.description}</p>
              </div>
              {isAdmin && (
                <label className="btn-primary cursor-pointer self-start sm:self-auto shrink-0">
                  <Upload size={16} />
                  {uploading ? '上传中…' : '上传文档'}
                  <input type="file" className="hidden" multiple onChange={upload} disabled={uploading} />
                </label>
              )}
            </div>

            <div className="card divide-y divide-gray-100">
              {docs.map((doc) => (
                <div key={doc.id} className="flex items-center gap-2 md:gap-3 px-3 md:px-4 py-3 cursor-pointer hover:bg-gray-50 transition-colors" onClick={() => navigate(`/docs/${doc.id}?chunk=0`)}>
                  <FileText size={18} className="text-gray-400 shrink-0" />
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-gray-800 truncate">{doc.title}</div>
                    <div className="text-xs text-gray-400 mt-0.5">
                      {formatDate(doc.created_at)} · {Math.round(doc.file_size / 1024)} KB · {doc.chunk_count} 分块
                    </div>
                  </div>
                  <span
                    className={cn(
                      'text-xs px-2 py-0.5 rounded-full shrink-0',
                      doc.status === 'ready'
                        ? 'bg-green-50 text-green-600'
                        : doc.status === 'failed'
                          ? 'bg-red-50 text-red-600'
                          : 'bg-amber-50 text-amber-600',
                    )}
                  >
                    {doc.status === 'ready' ? '已就绪' : doc.status === 'failed' ? '失败' : '处理中'}
                  </span>
                  <button
                    onClick={(e) => {
                      e.stopPropagation()
                      navigate(`/docs/${doc.id}?chunk=0`)
                    }}
                    className="p-1.5 rounded text-gray-400 hover:text-brand-600 hover:bg-gray-100 shrink-0"
                    title="查看文档内容"
                  >
                    <Eye size={14} />
                  </button>
                  {isAdmin && (
                    <div className="flex items-center gap-1 shrink-0">
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          reprocess(doc)
                        }}
                        className="p-1.5 rounded text-gray-400 hover:text-brand-600 hover:bg-gray-100"
                        title="重新处理"
                      >
                        <RefreshCw size={14} />
                      </button>
                      <button
                        onClick={(e) => {
                          e.stopPropagation()
                          deleteDoc(doc)
                        }}
                        className="p-1.5 rounded text-gray-400 hover:text-red-500 hover:bg-gray-100"
                        title="删除"
                      >
                        <Trash2 size={14} />
                      </button>
                    </div>
                  )}
                </div>
              ))}
              {docs.length === 0 && (
                <div className="text-center text-gray-400 text-sm py-10">
                  暂无文档，上传 txt / md / csv / docx / pdf 开始构建知识库
                </div>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
