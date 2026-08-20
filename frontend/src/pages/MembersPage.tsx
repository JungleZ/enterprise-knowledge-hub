import { useEffect, useState } from 'react'
import { Plus, Trash2 } from 'lucide-react'
import { api, type User } from '../api/client'
import { roleLabel, formatDate, isAdminRole } from '../lib/utils'
import { useAuthStore } from '../stores/auth'

export default function MembersPage() {
  const { user: me } = useAuthStore()
  const isAdmin = isAdminRole(me?.role)
  const [members, setMembers] = useState<User[]>([])
  const [showCreate, setShowCreate] = useState(false)
  const [form, setForm] = useState({ email: '', name: '', password: '', role: 'member', department: '全员', title: '' })

  const load = () => api.members.list().then(setMembers).catch(() => {})
  useEffect(() => {
    load()
  }, [])

  const create = async () => {
    if (!form.email || !form.password) return
    await api.members.create(form)
    setShowCreate(false)
    setForm({ email: '', name: '', password: '', role: 'member', department: '全员', title: '' })
    load()
  }

  const updateRole = async (m: User, role: string) => {
    await api.members.update(m.id, { role })
    load()
  }

  const remove = async (m: User) => {
    if (!confirm(`确认删除成员「${m.name}」？`)) return
    await api.members.remove(m.id)
    load()
  }

  return (
    <div className="h-full overflow-y-auto p-4 md:p-6">
      <div className="max-w-4xl mx-auto">
        <div className="flex items-center justify-between mb-4">
          <h2 className="text-lg font-semibold">成员管理</h2>
          {isAdmin && (
            <button onClick={() => setShowCreate(true)} className="btn-primary !px-3 !py-2">
              <Plus size={16} />
              <span className="hidden sm:inline">添加成员</span>
              <span className="sm:hidden">添加</span>
            </button>
          )}
        </div>

        {showCreate && (
          <div className="card p-4 mb-4 grid grid-cols-1 sm:grid-cols-2 gap-3">
            <input className="input" placeholder="邮箱 *" value={form.email} onChange={(e) => setForm({ ...form, email: e.target.value })} />
            <input className="input" placeholder="姓名" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
            <input className="input" placeholder="初始密码 *" value={form.password} onChange={(e) => setForm({ ...form, password: e.target.value })} />
            <select className="input" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
              <option value="member">普通成员</option>
              <option value="knowledge_admin">知识管理员</option>
            </select>
            <input className="input" placeholder="部门（默认全员）" value={form.department} onChange={(e) => setForm({ ...form, department: e.target.value })} />
            <input className="input" placeholder="职位" value={form.title} onChange={(e) => setForm({ ...form, title: e.target.value })} />
            <div className="flex gap-2 sm:col-span-2">
              <button onClick={create} className="btn-primary">创建</button>
              <button onClick={() => setShowCreate(false)} className="btn-ghost">取消</button>
            </div>
          </div>
        )}

        <div className="card divide-y divide-gray-100">
          {members.map((m) => (
            <div key={m.id} className="flex items-center gap-3 px-3 md:px-4 py-3">
              <div className="w-9 h-9 rounded-full bg-brand-100 text-brand-600 flex items-center justify-center text-sm font-medium shrink-0">
                {m.name.slice(0, 1)}
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-sm font-medium text-gray-800">{m.name}</span>
                  <span className="text-xs bg-gray-100 text-gray-500 px-1.5 py-0.5 rounded">{roleLabel(m.role)}</span>
                  {m.role === 'super_admin' && <span className="text-xs text-gray-400">（超管）</span>}
                </div>
                <div className="text-xs text-gray-400 mt-0.5 truncate">
                  {m.email} · {m.department || '-'} {m.title ? `· ${m.title}` : ''} · {formatDate(m.created_at)}
                </div>
              </div>
              {isAdmin && m.role !== 'super_admin' && (
                <div className="flex flex-col sm:flex-row items-stretch sm:items-center gap-2 shrink-0">
                  <select
                    className="text-xs border border-gray-300 rounded px-1.5 py-1"
                    value={m.role}
                    onChange={(e) => updateRole(m, e.target.value)}
                  >
                    <option value="member">普通成员</option>
                    <option value="knowledge_admin">知识管理员</option>
                  </select>
                  <button
                    onClick={() => remove(m)}
                    className="p-1.5 rounded text-gray-400 hover:text-red-500 hover:bg-gray-100"
                    title="删除成员"
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  )
}
