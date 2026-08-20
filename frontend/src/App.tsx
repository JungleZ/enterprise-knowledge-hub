import { useEffect } from 'react'
import { Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuthStore } from './stores/auth'
import { isAdminRole } from './lib/utils'
import Layout from './components/Layout'
import LoginPage from './pages/LoginPage'
import ChatPage from './pages/ChatPage'
import KBPage from './pages/KBPage'
import AdminPage from './pages/AdminPage'
import MembersPage from './pages/MembersPage'
import BotBindingsPage from './pages/BotBindingsPage'
import DocumentViewPage from './pages/DocumentViewPage'

function RequireAuth({ children }: { children: JSX.Element }) {
  const { isAuthenticated, user, loadUser } = useAuthStore()
  const location = useLocation()

  useEffect(() => {
    if (isAuthenticated && !user) {
      loadUser()
    }
  }, [isAuthenticated, user, loadUser])

  if (!isAuthenticated) {
    return <Navigate to="/login" state={{ from: location }} replace />
  }
  if (!user) {
    return <div className="flex items-center justify-center min-h-screen text-gray-400">加载中…</div>
  }
  return children
}

function RequireAdmin({ children }: { children: JSX.Element }) {
  const { user } = useAuthStore()
  if (!isAdminRole(user?.role)) {
    return <Navigate to="/chat" replace />
  }
  return children
}

export default function App() {
  return (
    <Routes>
      <Route path="/login" element={<LoginPage />} />
      <Route
        path="/"
        element={
          <RequireAuth>
            <Layout />
          </RequireAuth>
        }
      >
        <Route index element={<Navigate to="/chat" replace />} />
        <Route path="chat" element={<ChatPage />} />
        <Route path="chat/:sessionId" element={<ChatPage />} />
        <Route path="docs/:docId" element={<DocumentViewPage />} />
        <Route path="kbs" element={<KBPage />} />
        <Route path="members" element={<MembersPage />} />
        <Route
          path="bot-bindings"
          element={
            <RequireAdmin>
              <BotBindingsPage />
            </RequireAdmin>
          }
        />
        <Route
          path="admin"
          element={
            <RequireAdmin>
              <AdminPage />
            </RequireAdmin>
          }
        />
      </Route>
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}
