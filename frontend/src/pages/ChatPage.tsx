import { useEffect, useRef, useState } from 'react'
import { useParams, useNavigate, Link } from 'react-router-dom'
import {
  Plus,
  Trash2,
  ThumbsUp,
  ThumbsDown,
  Send,
  FileText,
  Globe,
  Menu,
  X,
  Mail,
} from 'lucide-react'
import { api, type ChatMessage, type ChatSession, type KnowledgeBase, type Citation, type ContactAdmin } from '../api/client'
import { cn, formatDate } from '../lib/utils'

interface LocalMsg extends ChatMessage {}

export default function ChatPage() {
  const { sessionId } = useParams()
  const navigate = useNavigate()
  const [sessions, setSessions] = useState<ChatSession[]>([])
  const [kbs, setKbs] = useState<KnowledgeBase[]>([])
  const [messages, setMessages] = useState<LocalMsg[]>([])
  const [input, setInput] = useState('')
  const [selectedKB, setSelectedKB] = useState('')
  const [webSearch, setWebSearch] = useState(false)
  const [sending, setSending] = useState(false)
  const [contacts, setContacts] = useState<ContactAdmin[]>([])
  const [showSessions, setShowSessions] = useState(false)
  const bottomRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    api.chat.sessions().then(setSessions).catch(() => {})
    api.kbs.list().then(setKbs).catch(() => {})
    api.admin.contact().then((r) => setContacts(r.admins)).catch(() => {})
  }, [])

  useEffect(() => {
    if (sessionId) {
      api.chat.messages(sessionId).then(setMessages).catch(() => {})
    } else {
      setMessages([])
    }
  }, [sessionId])

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages, sending])

  const send = async (question?: string, webSearchOverride?: boolean) => {
    const q = (question ?? input).trim()
    if (!q || sending) return
    const useWeb = webSearchOverride ?? webSearch
    setSending(true)
    setInput('')
    setShowSessions(false)
    const history = messages
      .filter((m) => m.content)
      .slice(-10)
      .map((m) => ({ role: m.role, content: m.content }))

    // local placeholders so the user msg + streaming AI msg appear instantly
    const tmpUserId = `tmp-user-${Date.now()}`
    const tmpAiId = `tmp-ai-${Date.now()}`
    const now = new Date().toISOString()
    setMessages((prev) => [
      ...prev,
      { id: tmpUserId, session_id: sessionId || '', role: 'user', content: q, citations: [], created_at: now },
      { id: tmpAiId, session_id: sessionId || '', role: 'assistant', content: '', citations: [], created_at: now },
    ])

    try {
      await api.chat.askStream(
        { session_id: sessionId, kb_id: selectedKB, question: q, web_search: useWeb, history },
        {
          onMeta: (meta) => {
            // fix the persisted user message id (only relevant for new sessions)
            setMessages((prev) =>
              prev.map((m) => (m.id === tmpUserId ? { ...m, id: meta.user_msg_id, session_id: meta.session_id } : m)),
            )
          },
          onDelta: (content) => {
            setMessages((prev) =>
              prev.map((m) => (m.id === tmpAiId ? { ...m, content: m.content + content } : m)),
            )
          },
          onDone: (done) => {
            setMessages((prev) =>
              prev.map((m) =>
                m.id === tmpAiId
                  ? { ...m, id: done.message_id, session_id: done.session_id, content: done.answer, is_missed: done.is_missed, citations: done.citations || [] }
                  : m,
              ),
            )
            if (!sessionId) {
              setSessions((prev) => {
                const existing = prev.find((s) => s.id === done.session_id)
                if (existing) return prev
                return [
                  { id: done.session_id, title: q.slice(0, 20), created_at: new Date().toISOString(), updated_at: new Date().toISOString() },
                  ...prev,
                ]
              })
              navigate(`/chat/${done.session_id}`, { replace: true })
            }
          },
          onError: (err) => {
            alert(err.message || '提问失败')
            // drop the incomplete AI placeholder if nothing streamed yet
            setMessages((prev) =>
              prev.map((m) => (m.id === tmpAiId && !m.content ? { ...m, content: '（回答生成失败，请重试）' } : m)),
            )
          },
        },
      )
    } catch (err: any) {
      alert(err.message || '提问失败')
      setMessages((prev) => prev.filter((m) => m.id !== tmpAiId && m.id !== tmpUserId))
    } finally {
      setSending(false)
    }
  }

  const retryWithWeb = (question: string) => {
    setWebSearch(true)
    send(question, true)
  }

  const newChat = () => {
    setMessages([])
    setInput('')
    navigate('/chat')
    setShowSessions(false)
  }

  const deleteSession = async (id: string, e: React.MouseEvent) => {
    e.stopPropagation()
    if (!confirm('确认删除该会话？')) return
    await api.chat.deleteSession(id)
    setSessions((prev) => prev.filter((s) => s.id !== id))
    if (sessionId === id) {
      newChat()
    }
  }

  const feedback = async (msg: ChatMessage, value: string) => {
    if (msg.feedback && msg.feedback !== 'none') return
    await api.chat.feedback(msg.id, value, '')
    setMessages((prev) => prev.map((m) => (m.id === msg.id ? { ...m, feedback: value } : m)))
  }

  // findQuestionFor returns the user message preceding the given assistant message.
  const findQuestionFor = (msg: ChatMessage): string => {
    const idx = messages.findIndex((m) => m.id === msg.id)
    if (idx <= 0) return ''
    for (let i = idx - 1; i >= 0; i--) {
      if (messages[i].role === 'user') return messages[i].content
    }
    return ''
  }

  const SessionList = (
    <div className="flex flex-col h-full">
      <div className="flex items-center justify-between p-3 border-b border-gray-100">
        <span className="text-sm font-medium text-gray-700">会话</span>
        <button onClick={newChat} className="btn-primary !py-1.5 text-xs">
          <Plus size={14} />
          新建
        </button>
      </div>
      <div className="flex-1 overflow-y-auto py-2">
        {sessions.map((s) => (
          <div
            key={s.id}
            onClick={() => {
              navigate(`/chat/${s.id}`)
              setShowSessions(false)
            }}
            className={cn(
              'group flex items-center justify-between px-3 py-2 mx-2 rounded-lg cursor-pointer text-sm',
              sessionId === s.id ? 'bg-brand-50 text-brand-700' : 'hover:bg-gray-100 text-gray-700',
            )}
          >
            <span className="truncate">{s.title}</span>
            <button
              onClick={(e) => deleteSession(s.id, e)}
              className="opacity-0 group-hover:opacity-100 text-gray-400 hover:text-red-500 transition-opacity"
              title="删除会话"
            >
              <Trash2 size={14} />
            </button>
          </div>
        ))}
        {sessions.length === 0 && <div className="text-center text-gray-400 text-xs mt-8">暂无会话</div>}
      </div>
    </div>
  )

  return (
    <div className="flex h-full bg-gray-50">
      {/* sidebar: sessions (desktop) */}
      <div className="hidden md:flex w-64 shrink-0 border-r border-gray-200 bg-white flex-col">
        <div className="p-3 border-b border-gray-100">
          <button onClick={newChat} className="btn-primary w-full">
            <Plus size={16} />
            新建对话
          </button>
        </div>
        {SessionList}
      </div>

      {/* mobile sessions drawer */}
      {showSessions && (
        <div className="md:hidden fixed inset-0 z-40">
          <div className="absolute inset-0 bg-black/40" onClick={() => setShowSessions(false)} />
          <div className="absolute left-0 top-0 bottom-0 w-72 bg-white shadow-2xl">{SessionList}</div>
        </div>
      )}

      {/* main chat area */}
      <div className="flex-1 flex flex-col min-w-0">
        <div className="flex items-center gap-2 px-3 md:px-4 py-2.5 border-b border-gray-200 bg-white">
          <button
            onClick={() => setShowSessions(true)}
            className="md:hidden p-1.5 rounded hover:bg-gray-100 text-gray-500 transition-colors"
            title="会话列表"
          >
            <Menu size={18} />
          </button>
          <select
            className="input !py-1.5 text-sm min-w-0 flex-1 md:flex-none md:max-w-xs"
            value={selectedKB}
            onChange={(e) => setSelectedKB(e.target.value)}
          >
            <option value="">全部知识库</option>
            {kbs.map((kb) => (
              <option key={kb.id} value={kb.id}>
                {kb.name}
              </option>
            ))}
          </select>
          <span className="hidden sm:inline text-xs text-gray-400 shrink-0">
            范围：{selectedKB ? '指定知识库' : '全部'}
          </span>
        </div>

        <div className="flex-1 overflow-y-auto px-3 md:px-4 py-4 md:py-6">
          <div className="max-w-3xl mx-auto space-y-6">
            {messages.length === 0 && (
              <div className="text-center text-gray-400 mt-16 md:mt-20">
                <div className="text-4xl mb-3">💡</div>
                <p>输入问题开始提问，例如：</p>
                <div className="mt-2 space-y-1 text-sm">
                  <p>「公司的休假制度是怎样的？」</p>
                  <p>「新员工入职流程有哪些步骤？」</p>
                </div>
              </div>
            )}

            {messages.map((m) =>
              m.role === 'user' ? (
                <div key={m.id} className="flex justify-end">
                  <div className="max-w-[85%] md:max-w-[75%] bg-brand-600 text-white rounded-2xl rounded-tr-sm px-3.5 py-2.5 text-sm whitespace-pre-wrap">
                    {m.content}
                  </div>
                </div>
              ) : (
                <div key={m.id} className="flex gap-2.5 md:gap-3">
                  <div className="w-8 h-8 rounded-full bg-gray-800 text-white flex items-center justify-center text-xs shrink-0">
                    AI
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="bg-white border border-gray-200 rounded-2xl rounded-tl-sm px-3 py-3 md:px-4 shadow-sm">
                      <div className="text-sm whitespace-pre-wrap leading-relaxed break-words">
                        {m.is_missed && (
                          <div className="text-xs text-amber-600 mb-2 bg-amber-50 rounded-md px-2 py-1">
                            未能从知识库中找到足够相关的资料
                            {webSearch && '，已尝试联网搜索'}：
                          </div>
                        )}
                        {m.content}
                      </div>

                      {m.citations && m.citations.length > 0 && (
                        <div className="mt-3 pt-3 border-t border-gray-100">
                          <div className="text-xs text-gray-400 mb-2">引用来源</div>
                          <div className="space-y-1.5">
                            {m.citations.map((c: Citation, ci: number) => (
                              <div key={c.chunk_id || ci} className="text-xs bg-gray-50 rounded-lg px-3 py-2">
                                {c.url ? (
                                  <div className="flex items-start gap-1.5 text-gray-700 font-medium">
                                    <Globe size={12} className="mt-0.5 shrink-0 text-brand-500" />
                                    <a href={c.url} target="_blank" rel="noopener noreferrer" className="hover:text-brand-600 hover:underline break-all">
                                      {c.doc_title}
                                    </a>
                                  </div>
                                ) : (
                                  <div className="flex items-start gap-1.5 text-gray-700 font-medium">
                                    <FileText size={12} className="mt-0.5 shrink-0" />
                                    <Link
                                      to={`/docs/${c.doc_id}?chunk=${c.chunk_index ?? 0}`}
                                      className="hover:text-brand-600 hover:underline"
                                    >
                                      {c.doc_title}
                                    </Link>
                                    {typeof c.chunk_index === 'number' && (
                                      <span className="text-gray-400 shrink-0">第 {c.chunk_index + 1} 段</span>
                                    )}
                                  </div>
                                )}
                                <div className="text-gray-500 mt-1 line-clamp-2">{c.snippet}</div>
                              </div>
                            ))}
                          </div>
                        </div>
                      )}

                      {m.is_missed && (
                        <div className="mt-3 pt-3 border-t border-gray-100 text-xs">
                          <div className="text-gray-500 mb-2">
                            如果没有找到答案，可以试试：
                          </div>
                          <div className="flex flex-wrap items-center gap-2">
                            <button
                              onClick={() => retryWithWeb(findQuestionFor(m))}
                              className="btn-ghost !py-1.5 text-xs"
                              title="开启联网搜索并重新提问"
                            >
                              <Globe size={14} />
                              联网搜索重试
                            </button>
                            {contacts.length > 0 && (
                              <>
                                <span className="text-gray-400">·</span>
                                <a
                                  href={`mailto:${contacts[0].email}?subject=知识库未命中反馈&body=问题：${encodeURIComponent(
                                    findQuestionFor(m),
                                  )}%0A%0A请补充相关知识库内容，谢谢！`}
                                  className="inline-flex items-center gap-1.5 btn-ghost !py-1.5 text-xs"
                                >
                                  <Mail size={14} />
                                  联系管理员（{contacts[0].name}）
                                </a>
                              </>
                            )}
                          </div>
                        </div>
                      )}
                    </div>
                    <div className="flex items-center gap-1 mt-1.5 text-gray-400">
                      <button
                        onClick={() => feedback(m, 'up')}
                        className={cn(
                          'p-1 rounded hover:bg-gray-100 transition-colors',
                          m.feedback === 'up' && 'text-brand-600',
                        )}
                        title="有帮助"
                      >
                        <ThumbsUp size={14} />
                      </button>
                      <button
                        onClick={() => feedback(m, 'down')}
                        className={cn(
                          'p-1 rounded hover:bg-gray-100 transition-colors',
                          m.feedback === 'down' && 'text-red-500',
                        )}
                        title="没帮助"
                      >
                        <ThumbsDown size={14} />
                      </button>
                      <span className="text-xs ml-1">{formatDate(m.created_at)}</span>
                    </div>
                  </div>
                </div>
              ),
            )}

            {sending && (
              <div className="flex gap-3">
                <div className="w-8 h-8 rounded-full bg-gray-800 text-white flex items-center justify-center text-xs">AI</div>
                <div className="bg-white border border-gray-200 rounded-2xl rounded-tl-sm px-4 py-3 text-sm text-gray-400">
                  正在思考…
                </div>
              </div>
            )}
            <div ref={bottomRef} />
          </div>
        </div>

        <div className="px-3 md:px-4 py-3 border-t border-gray-200 bg-white">
          <div className="max-w-3xl mx-auto">
            <div className="flex items-center gap-2 mb-1.5">
              <button
                onClick={() => setWebSearch((v) => !v)}
                className={cn(
                  'inline-flex items-center gap-1.5 text-xs px-2.5 py-1 rounded-full border transition-colors',
                  webSearch
                    ? 'bg-brand-600 border-brand-600 text-white'
                    : 'bg-gray-50 border-gray-200 text-gray-500 hover:border-brand-300 hover:text-brand-600',
                )}
                title="开启后，知识库检索不到时会联网搜索补充答案"
              >
                <Globe size={13} />
                联网搜索
              </button>
              <span className="text-xs text-gray-400 hidden sm:inline">
                {webSearch ? '已开启：知识库未命中时自动联网' : '知识库检索不到时可开启联网'}
              </span>
            </div>
            <div className="flex gap-2">
              <textarea
                className="input resize-none"
                rows={2}
                placeholder="输入你的问题，Enter 发送，Shift+Enter 换行"
                value={input}
                onChange={(e) => setInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' && !e.shiftKey) {
                    e.preventDefault()
                    send()
                  }
                }}
              />
              <button onClick={() => send()} disabled={sending || !input.trim()} className="btn-primary shrink-0 self-end">
                <Send size={16} />
                发送
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}