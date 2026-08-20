import { useState } from 'react'
import { NavLink, Outlet, useNavigate } from 'react-router-dom'
import { BookOpen, MessageSquare, Users, BarChart3, Bot, LogOut, Menu, X } from 'lucide-react'
import { useAuthStore } from '../stores/auth'
import { isAdminRole, cn } from '../lib/utils'

const navItems = [
  { to: '/chat', label: '智能问答', icon: MessageSquare },
  { to: '/kbs', label: '知识库管理', icon: BookOpen },
  { to: '/members', label: '成员管理', icon: Users },
]

function NavLinks({ onClick }: { onClick?: () => void }) {
  const { user } = useAuthStore()
  return (
    <>
      {navItems.map((item) => (
        <NavLink
          key={item.to}
          to={item.to}
          onClick={onClick}
          className={({ isActive }) =>
            cn(
              'flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors',
              isActive ? 'bg-gray-800 text-white' : 'hover:bg-gray-800/60 hover:text-white',
            )
          }
        >
          <item.icon size={16} />
          {item.label}
        </NavLink>
      ))}
      {isAdminRole(user?.role) && (
        <>
          <NavLink
            to="/admin"
            onClick={onClick}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors',
                isActive ? 'bg-gray-800 text-white' : 'hover:bg-gray-800/60 hover:text-white',
              )
            }
          >
            <BarChart3 size={16} />
            管理后台
          </NavLink>
          <NavLink
            to="/bot-bindings"
            onClick={onClick}
            className={({ isActive }) =>
              cn(
                'flex items-center gap-2 px-3 py-2 rounded-md text-sm transition-colors',
                isActive ? 'bg-gray-800 text-white' : 'hover:bg-gray-800/60 hover:text-white',
              )
            }
          >
            <Bot size={16} />
            机器人绑定
          </NavLink>
        </>
      )}
    </>
  )
}

export default function Layout() {
  const { user, tenant, logout } = useAuthStore()
  const navigate = useNavigate()
  const [mobileOpen, setMobileOpen] = useState(false)

  const doLogout = () => {
    logout()
    navigate('/login')
  }

  return (
    <div className="flex h-screen">
      {/* desktop sidebar */}
      <aside className="hidden md:flex w-56 shrink-0 bg-gray-900 text-gray-300 flex-col">
        <div className="px-4 py-5 border-b border-gray-800">
          <div className="text-white font-semibold text-sm">企业知识库</div>
          <div className="text-gray-500 text-xs mt-0.5 truncate">{tenant?.name}</div>
        </div>
        <nav className="flex-1 py-3 space-y-1 px-2 overflow-y-auto">
          <NavLinks />
        </nav>
        <div className="p-4 border-t border-gray-800 flex items-center justify-between">
          <div className="min-w-0">
            <div className="text-sm text-white truncate">{user?.name}</div>
            <div className="text-xs text-gray-500 truncate">{user?.email}</div>
          </div>
          <button
            onClick={doLogout}
            className="text-gray-500 hover:text-white transition-colors"
            title="退出登录"
          >
            <LogOut size={16} />
          </button>
        </div>
      </aside>

      {/* mobile top bar */}
      <div className="md:hidden fixed top-0 inset-x-0 z-40 bg-gray-900 text-gray-300 flex items-center justify-between px-3 py-2.5 shadow-md">
        <button
          onClick={() => setMobileOpen(true)}
          className="p-1.5 rounded hover:bg-gray-800 transition-colors"
          title="打开菜单"
        >
          <Menu size={20} />
        </button>
        <div className="min-w-0 text-center">
          <div className="text-white font-semibold text-sm leading-tight truncate">企业知识库</div>
          <div className="text-gray-500 text-[11px] truncate">{tenant?.name}</div>
        </div>
        <button
          onClick={doLogout}
          className="p-1.5 rounded hover:bg-gray-800 transition-colors"
          title="退出登录"
        >
          <LogOut size={18} />
        </button>
      </div>

      {/* mobile drawer */}
      {mobileOpen && (
        <div className="md:hidden fixed inset-0 z-50">
          <div className="absolute inset-0 bg-black/50" onClick={() => setMobileOpen(false)} />
          <aside className="absolute left-0 top-0 bottom-0 w-64 bg-gray-900 text-gray-300 flex flex-col shadow-2xl">
            <div className="px-4 py-4 border-b border-gray-800 flex items-center justify-between">
              <div className="min-w-0">
                <div className="text-white font-semibold text-sm truncate">企业知识库</div>
                <div className="text-gray-500 text-xs mt-0.5 truncate">{tenant?.name}</div>
              </div>
              <button
                onClick={() => setMobileOpen(false)}
                className="p-1.5 rounded hover:bg-gray-800 transition-colors"
                title="关闭菜单"
              >
                <X size={18} />
              </button>
            </div>
            <nav className="flex-1 py-3 space-y-1 px-2 overflow-y-auto">
              <NavLinks onClick={() => setMobileOpen(false)} />
            </nav>
            <div className="p-4 border-t border-gray-800">
              <div className="text-sm text-white truncate">{user?.name}</div>
              <div className="text-xs text-gray-500 truncate">{user?.email}</div>
            </div>
          </aside>
        </div>
      )}

      <main className="flex-1 overflow-hidden pt-11 md:pt-0">
        <Outlet />
      </main>
    </div>
  )
}