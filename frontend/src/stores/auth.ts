import { create } from 'zustand'
import { api, type User, type Tenant } from '../api/client'
import { getToken, setToken, removeToken } from '../lib/utils'

interface AuthState {
  user: User | null
  tenant: Tenant | null
  isAuthenticated: boolean
  login: (email: string, password: string) => Promise<void>
  register: (data: { tenant_name: string; company: string; email: string; password: string; name: string }) => Promise<void>
  logout: () => void
  loadUser: () => Promise<void>
}

export const useAuthStore = create<AuthState>((set) => ({
  user: null,
  tenant: null,
  isAuthenticated: !!getToken(),

  login: async (email, password) => {
    const res = await api.auth.login(email, password)
    setToken(res.token)
    set({ user: res.user, tenant: res.tenant, isAuthenticated: true })
  },

  register: async (data) => {
    const res = await api.auth.register(data)
    setToken(res.token)
    set({ user: res.user, tenant: res.tenant, isAuthenticated: true })
  },

  logout: () => {
    removeToken()
    set({ user: null, tenant: null, isAuthenticated: false })
  },

  loadUser: async () => {
    try {
      const { user, tenant } = await api.auth.me()
      set({ user, tenant, isAuthenticated: true })
    } catch {
      removeToken()
      set({ user: null, tenant: null, isAuthenticated: false })
    }
  },
}))
