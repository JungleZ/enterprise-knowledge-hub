import { useEffect, useState } from 'react'
import { api, type BotBinding } from '../api/client'
import { formatDate } from '../lib/utils'

const statusMeta: Record<string, { label: string; cls: string }> = {
  pending: { label: '待审批', cls: 'bg-amber-50 text-amber-600' },
  approved: { label: '已绑定', cls: 'bg-green-50 text-green-600' },
  rejected: { label: '已拒绝', cls: 'bg-red-50 text-red-600' },
}

export default function BotBindingsPage() {
  const [bindings, setBindings] = useState<BotBinding[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const load = () => {
    api.bot
      .list()
      .then(setBindings)
      .catch((e) => setError(e.message))
      .finally(() => setLoading(false))
  }

  useEffect(load, [])

  const decide = async (b: BotBinding, status: string) => {
    try {
      await api.bot.decide(b.email, status)
      load()
    } catch (e: any) {
      setError(e.message)
    }
  }

  const unbind = async (b: BotBinding) => {
    if (!window.confirm(`确定解绑 ${b.user_name || b.email} 吗？解绑后需重新绑定。`)) return
    try {
      await api.bot.unbind(b.id)
      load()
    } catch (e: any) {
      setError(e.message)
    }
  }

  return (
    <div className="h-full overflow-y-auto p-4 md:p-6">
      <div className="max-w-3xl mx-auto">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold">机器人绑定审批</h2>
          <button onClick={load} className="btn-ghost">
            刷新
          </button>
        </div>
        <p className="text-sm text-gray-500 mb-4">
          员工在飞书里给机器人发送「绑定 &lt;系统邮箱&gt;」后，在此批准/拒绝。批准后该员工即可在飞书里用其角色的权限提问。
        </p>

        {error && <div className="text-sm text-red-600 mb-3">{error}</div>}

        {!loading && bindings.length === 0 && (
          <div className="card text-center text-gray-400 text-sm py-10">暂无绑定记录</div>
        )}

        <div className="card divide-y divide-gray-100">
          {bindings.map((b) => {
            const meta = statusMeta[b.status] || statusMeta.pending
            return (
              <div key={b.id} className="px-3 md:px-4 py-3">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm font-medium text-gray-800 break-all">{b.user_name || b.email}</span>
                  <span className={`text-xs px-2 py-0.5 rounded-full ${meta.cls}`}>{meta.label}</span>
                </div>
                <div className="text-xs text-gray-400 mt-0.5 truncate">
                  {b.email} · 飞书 {b.open_id.slice(0, 8)}… · {formatDate(b.created_at)}
                </div>

                <div className="mt-2">
                  {b.status === 'pending' && (
                    <div className="flex gap-2">
                      <button onClick={() => decide(b, 'approved')} className="btn-primary !py-1.5 !px-3">
                        批准
                      </button>
                      <button onClick={() => decide(b, 'rejected')} className="btn-ghost !py-1.5 !px-3">
                        拒绝
                      </button>
                    </div>
                  )}

                  {b.status === 'approved' && (
                    <button onClick={() => unbind(b)} className="btn-ghost !py-1.5 !px-3">
                      解绑
                    </button>
                  )}
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}
