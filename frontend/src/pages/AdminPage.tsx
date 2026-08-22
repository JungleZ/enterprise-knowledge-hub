import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ChevronRight, ChevronDown, ChevronUp } from 'lucide-react'
import { api, type Stats, type Gap, type AuditLog, type ChatMessage, type SessionWithMeta } from '../api/client'
import { formatDate } from '../lib/utils'

function StatCard({
  label,
  value,
  accent,
  onClick,
  hint,
}: {
  label: string
  value: string | number
  accent?: string
  onClick?: () => void
  hint?: string
}) {
  const content = (
    <>
      <div className={`text-2xl font-bold ${accent || 'text-gray-900'}`}>{value}</div>
      <div className="text-xs text-gray-500 mt-1 flex items-center gap-1">
        {label}
        {hint && <ChevronRight size={12} className="opacity-60" />}
      </div>
    </>
  )
  if (!onClick) {
    return <div className="card p-4">{content}</div>
  }
  return (
    <button
      onClick={onClick}
      className="card p-4 text-left hover:shadow-md hover:border-brand-200 transition-all cursor-pointer active:scale-[0.98] group"
      title={hint || '点击查看详情'}
    >
      {content}
    </button>
  )
}

export default function AdminPage() {
  const navigate = useNavigate()
  const [stats, setStats] = useState<Stats | null>(null)
  const [gaps, setGaps] = useState<Gap[]>([])
  const [audit, setAudit] = useState<AuditLog[]>([])
  const [feedback, setFeedback] = useState<ChatMessage[]>([])
  const [sessions, setSessions] = useState<SessionWithMeta[]>([])
  const [openSession, setOpenSession] = useState<string | null>(null)
  const [sessionMsgs, setSessionMsgs] = useState<ChatMessage[]>([])
  const [loadingMsgs, setLoadingMsgs] = useState(false)
  const [tab, setTab] = useState<'overview' | 'gaps' | 'feedback' | 'audit' | 'sessions'>('overview')

  useEffect(() => {
    api.admin.stats().then(setStats).catch(() => {})
    api.admin.gaps().then(setGaps).catch(() => {})
    api.admin.audit().then(setAudit).catch(() => {})
    api.admin.feedback().then(setFeedback).catch(() => {})
    api.admin.sessions().then(setSessions).catch(() => {})
  }, [])

  const toggleSession = async (id: string) => {
    if (openSession === id) {
      setOpenSession(null)
      setSessionMsgs([])
      return
    }
    setOpenSession(id)
    setLoadingMsgs(true)
    try {
      const msgs = await api.admin.sessionMessages(id)
      setSessionMsgs(msgs)
    } catch {
      setSessionMsgs([])
    } finally {
      setLoadingMsgs(false)
    }
  }

  const tabs: [typeof tab, string][] = [
    ['overview', '知识缺口'],
    ['sessions', '会话记录'],
    ['feedback', '用户反馈'],
    ['audit', '操作审计'],
  ]

  return (
    <div className="h-full overflow-y-auto p-4 md:p-6">
      <div className="max-w-5xl mx-auto">
        <h2 className="text-lg font-semibold mb-4">管理后台</h2>

        {/* stats */}
        <div className="grid grid-cols-2 md:grid-cols-4 gap-3 mb-6">
          <StatCard
            label="文档总数"
            value={stats?.total_docs ?? 0}
            onClick={() => navigate('/kbs')}
            hint="前往知识库管理查看全部文档"
          />
          <StatCard
            label="已就绪文档"
            value={stats?.ready_docs ?? 0}
            accent="text-green-600"
            onClick={() => navigate('/kbs')}
            hint="前往知识库管理查看文档状态"
          />
          <StatCard
            label="知识分块"
            value={stats?.total_chunks ?? 0}
            accent="text-brand-600"
            onClick={() => navigate('/kbs')}
            hint="前往知识库管理查看分块详情"
          />
          <StatCard
            label="知识库"
            value={stats?.total_kbs ?? 0}
            onClick={() => navigate('/kbs')}
            hint="前往知识库管理"
          />
          <StatCard
            label="成员"
            value={stats?.total_members ?? 0}
            onClick={() => navigate('/members')}
            hint="前往成员管理"
          />
          <StatCard
            label="会话数"
            value={stats?.total_sessions ?? 0}
            onClick={() => setTab('sessions')}
            hint="查看全部会话记录"
          />
          <StatCard
            label="未命中率"
            value={stats ? `${stats.miss_rate.toFixed(1)}%` : '0%'}
            accent="text-amber-600"
            onClick={() => setTab('gaps')}
            hint="查看未命中的知识缺口"
          />
          <StatCard
            label="正/负反馈"
            value={stats ? `${stats.positive_feedback}/${stats.negative_feedback}` : '0/0'}
            onClick={() => setTab('feedback')}
            hint="查看用户反馈详情"
          />
        </div>

        {/* tabs */}
        <div className="flex gap-2 mb-4 border-b border-gray-200 overflow-x-auto">
          {tabs.map(([key, label]) => (
            <button
              key={key}
              onClick={() => setTab(key)}
              className={`px-3 py-2 text-sm font-medium border-b-2 transition-colors whitespace-nowrap ${
                tab === key
                  ? 'border-brand-600 text-brand-600'
                  : 'border-transparent text-gray-500 hover:text-gray-700'
              }`}
            >
              {label}
            </button>
          ))}
        </div>

        {tab === 'overview' && (
          <div className="card divide-y divide-gray-100">
            {gaps.length === 0 && <div className="text-center text-gray-400 text-sm py-8">暂无知识缺口，继续加油！</div>}
            {gaps.map((g, i) => (
              <div key={i} className="px-4 py-3">
                <div className="text-sm text-gray-800 break-words">{g.question}</div>
                <div className="text-xs text-gray-400 mt-0.5">
                  被问 {g.count} 次 · 最近 {formatDate(g.last_seen)}
                </div>
              </div>
            ))}
          </div>
        )}

        {tab === 'sessions' && (
          <div className="card divide-y divide-gray-100">
            {sessions.length === 0 && <div className="text-center text-gray-400 text-sm py-8">暂无会话记录</div>}
            {sessions.map((s) => (
              <div key={s.id}>
                <button
                  onClick={() => toggleSession(s.id)}
                  className="w-full flex items-center gap-3 px-4 py-3 text-left hover:bg-gray-50 transition-colors"
                >
                  <div className="flex-1 min-w-0">
                    <div className="text-sm font-medium text-gray-800 truncate">{s.title || '（无标题）'}</div>
                    <div className="text-xs text-gray-400 mt-0.5">
                      {s.user_name || s.user_email} · {s.msg_count} 条消息 · {formatDate(s.updated_at)}
                    </div>
                  </div>
                  {openSession === s.id ? (
                    <ChevronUp size={16} className="text-gray-400 shrink-0" />
                  ) : (
                    <ChevronDown size={16} className="text-gray-400 shrink-0" />
                  )}
                </button>
                {openSession === s.id && (
                  <div className="px-4 pb-4 space-y-2">
                    {loadingMsgs && <div className="text-xs text-gray-400">加载中…</div>}
                    {sessionMsgs.map((m) => (
                      <div key={m.id} className="text-xs rounded-lg bg-gray-50 px-3 py-2">
                        <span className={`font-medium ${m.role === 'user' ? 'text-brand-600' : 'text-gray-700'}`}>
                          {m.role === 'user' ? '用户' : 'AI'}
                        </span>
                        <span className="text-gray-400 ml-2">{formatDate(m.created_at)}</span>
                        <div className="text-gray-600 mt-1 whitespace-pre-wrap break-words">{m.content}</div>
                        {m.is_missed && <div className="text-amber-500 mt-1">未命中</div>}
                      </div>
                    ))}
                  </div>
                )}
              </div>
            ))}
          </div>
        )}

        {tab === 'feedback' && (
          <div className="card divide-y divide-gray-100">
            {feedback.length === 0 && <div className="text-center text-gray-400 text-sm py-8">暂无反馈</div>}
            {feedback.map((f) => (
              <div key={f.id} className="px-4 py-3">
                <div className="flex items-center gap-2 flex-wrap">
                  <span
                    className={`text-xs px-2 py-0.5 rounded-full ${
                      f.feedback === 'up' ? 'bg-green-50 text-green-600' : 'bg-red-50 text-red-600'
                    }`}
                  >
                    {f.feedback === 'up' ? '有帮助' : '没帮助'}
                  </span>
                  <span className="text-xs text-gray-400">{formatDate(f.created_at)}</span>
                </div>
                <div className="text-sm text-gray-700 mt-1.5 whitespace-pre-wrap break-words">{f.content}</div>
                {f.feedback_note && <div className="text-xs text-gray-400 mt-1">备注：{f.feedback_note}</div>}
              </div>
            ))}
          </div>
        )}

        {tab === 'audit' && (
          <div className="card divide-y divide-gray-100">
            {audit.length === 0 && <div className="text-center text-gray-400 text-sm py-8">暂无操作记录</div>}
            {audit.map((a) => (
              <div key={a.id} className="px-4 py-2.5 flex items-center gap-3 text-sm">
                <span className="text-gray-800 font-medium shrink-0">{a.user_name}</span>
                <span className="text-xs bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded shrink-0">{a.action}</span>
                <span className="text-gray-500 flex-1 min-w-0 truncate">{a.detail}</span>
                <span className="text-xs text-gray-400 shrink-0">{formatDate(a.created_at)}</span>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}