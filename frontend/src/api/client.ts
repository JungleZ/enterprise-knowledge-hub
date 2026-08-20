import { getToken, removeToken } from '../lib/utils'

const BASE_URL = '/api'

export interface User {
  id: string
  tenant_id: string
  email: string
  name: string
  role: string
  department?: string
  title?: string
  created_at: string
}

export interface Tenant {
  id: string
  name: string
  slug: string
  plan?: string
}

export interface KnowledgeBase {
  id: string
  tenant_id: string
  name: string
  description?: string
  allowed_departments?: string
  creator_id?: string
  created_at: string
  updated_at: string
}

export interface Document {
  id: string
  kb_id: string
  title: string
  filename: string
  file_size: number
  content_type?: string
  status: string
  error?: string
  chunk_count: number
  access_tags?: string
  created_at: string
}

export interface Citation {
  doc_id: string
  doc_title: string
  chunk_id: string
  snippet: string
  score: number
  chunk_text?: string
  chunk_index?: number
  url?: string
}

export interface ChatMessage {
  id: string
  session_id: string
  role: string
  content: string
  citations: Citation[]
  is_missed?: boolean
  feedback?: string
  feedback_note?: string
  created_at: string
}

export interface ChatSession {
  id: string
  title: string
  created_at: string
  updated_at: string
}

export interface AskResponse {
  SessionID: string
  UserMsg: ChatMessage
  AssistantMsg: ChatMessage
  Answer: string
  Citations: Citation[]
  IsMissed: boolean
}

export interface AskStreamMeta {
  session_id: string
  user_msg_id: string
  citations: Citation[]
  is_missed: boolean
}

export interface AskStreamDone {
  message_id: string
  session_id: string
  answer: string
  is_missed: boolean
  citations: Citation[]
}

export interface AskStreamEvents {
  onMeta?: (meta: AskStreamMeta) => void
  onDelta?: (content: string) => void
  onDone?: (done: AskStreamDone) => void
  onError?: (err: Error) => void
}

export interface AskStreamPayload {
  session_id?: string
  kb_id?: string
  question: string
  web_search?: boolean
  history: { role: string; content: string }[]
}

export interface DocChunk {
  id: string
  chunk_index: number
  text: string
}

export interface DocContent {
  doc_id: string
  doc_title: string
  kb_id: string
  chunk_count: number
  chunks: DocChunk[]
}

export interface SessionWithMeta extends ChatSession {
  user_name: string
  user_email: string
  msg_count: number
}

export interface ContactAdmin {
  name: string
  email: string
  title?: string
  department?: string
}

export interface Stats {
  tenant_name: string
  total_docs: number
  ready_docs: number
  failed_docs: number
  total_chunks: number
  total_kbs: number
  total_members: number
  total_sessions: number
  total_messages: number
  missed_messages: number
  miss_rate: number
  positive_feedback: number
  negative_feedback: number
}

export interface Gap {
  question: string
  count: number
  last_seen: string
}

export interface AuditLog {
  id: string
  user_name: string
  action: string
  detail: string
  created_at: string
}

export interface BotBinding {
  id: string
  tenant_id: string
  platform: string
  open_id: string
  user_id: string
  email: string
  status: string // pending | approved | rejected
  created_at: string
  updated_at: string
  user_name?: string
}

async function request<T>(path: string, options: RequestInit = {}): Promise<T> {
  const token = getToken()
  const headers: Record<string, string> = {
    ...(options.headers as Record<string, string>),
  }
  if (!(options.body instanceof FormData)) {
    headers['Content-Type'] = 'application/json'
  }
  if (token) {
    headers['Authorization'] = `Bearer ${token}`
  }

  const res = await fetch(`${BASE_URL}${path}`, {
    ...options,
    headers,
  })

  if (res.status === 401) {
    removeToken()
    window.location.href = '/login'
    throw new Error('Unauthorized')
  }

  if (res.status === 204) {
    return undefined as T
  }

  const data = await res.json().catch(() => null)
  if (!res.ok) {
    throw new Error((data && data.error) || '请求失败')
  }
  return data as T
}

export const api = {
  auth: {
    login: (email: string, password: string) =>
      request<{ token: string; user: User; tenant: Tenant }>('/auth/login', {
        method: 'POST',
        body: JSON.stringify({ email, password }),
      }),
    register: (data: { tenant_name: string; company: string; email: string; password: string; name: string }) =>
      request<{ token: string; user: User; tenant: Tenant }>('/auth/register', {
        method: 'POST',
        body: JSON.stringify(data),
      }),
    me: () => request<{ user: User; tenant: Tenant }>('/auth/me'),
  },

  kbs: {
    list: () => request<KnowledgeBase[]>('/kbs'),
    create: (data: { name: string; description: string; allowed_departments: string }) =>
      request<KnowledgeBase>('/kbs', { method: 'POST', body: JSON.stringify(data) }),
    update: (id: string, data: Partial<KnowledgeBase>) =>
      request<KnowledgeBase>(`/kbs/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
    remove: (id: string) => request<void>(`/kbs/${id}`, { method: 'DELETE' }),
  },

  docs: {
    list: (kbId: string) => request<Document[]>(`/kbs/${kbId}/docs`),
    upload: (kbId: string, file: File, title: string, accessTags: string) => {
      const form = new FormData()
      form.append('file', file)
      form.append('title', title)
      form.append('access_tags', accessTags)
      return request<Document>(`/kbs/${kbId}/docs`, { method: 'POST', body: form })
    },
    remove: (docId: string) => request<void>(`/docs/${docId}`, { method: 'DELETE' }),
    reprocess: (docId: string) => request<void>(`/docs/${docId}/reprocess`, { method: 'POST' }),
    chunks: (docId: string) => request<DocContent>(`/docs/${docId}/chunks`),
  },

  chat: {
    ask: (data: { session_id?: string; kb_id?: string; question: string; web_search?: boolean; history: { role: string; content: string }[] }) =>
      request<AskResponse>('/chat/ask', { method: 'POST', body: JSON.stringify(data) }),
    // askStream consumes the SSE endpoint (Accept: text/event-stream).
    // On non-SSE responses (e.g. auth error) it falls back to JSON and throws.
    askStream: async (data: AskStreamPayload, events: AskStreamEvents, signal?: AbortSignal) => {
      const token = getToken()
      const res = await fetch(`${BASE_URL}/chat/ask`, {
        method: 'POST',
        signal,
        headers: {
          'Content-Type': 'application/json',
          Accept: 'text/event-stream',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
        },
        body: JSON.stringify(data),
      })
      if (!res.ok || !res.body) {
        const errData = await res.json().catch(() => null)
        if (res.status === 401) {
          removeToken()
          window.location.href = '/login'
        }
        throw new Error((errData && errData.error) || `请求失败(${res.status})`)
      }
      const reader = res.body.getReader()
      const decoder = new TextDecoder()
      let buf = ''
      const emitFrame = (frame: string) => {
        const lines = frame.split('\n')
        const event = lines.find((l) => l.startsWith('event:'))?.slice(6).trim()
        const dataRaw = lines.find((l) => l.startsWith('data:'))?.slice(5).trim()
        if (!event || !dataRaw) return
        let payload: any
        try {
          payload = JSON.parse(dataRaw)
        } catch {
          return
        }
        switch (event) {
          case 'meta':
            events.onMeta?.(payload as AskStreamMeta)
            break
          case 'delta':
            events.onDelta?.(String(payload.content || ''))
            break
          case 'done':
            events.onDone?.(payload as AskStreamDone)
            break
          case 'error':
            events.onError?.(new Error(payload.error || '回答生成失败'))
            break
        }
      }
      while (true) {
        const { done, value } = await reader.read()
        buf += decoder.decode(value || new Uint8Array(), { stream: !done })
        let idx: number
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          emitFrame(buf.slice(0, idx))
          buf = buf.slice(idx + 2)
        }
        if (done) break
      }
      if (buf.trim()) emitFrame(buf)
    },
    sessions: () => request<ChatSession[]>('/chat/sessions'),
    messages: (sessionId: string) => request<ChatMessage[]>(`/chat/sessions/${sessionId}/messages`),
    feedback: (messageId: string, feedback: string, note: string) =>
      request<void>(`/chat/messages/${messageId}/feedback`, {
        method: 'POST',
        body: JSON.stringify({ feedback, note }),
      }),
    deleteSession: (sessionId: string) => request<void>(`/chat/sessions/${sessionId}`, { method: 'DELETE' }),
  },

  members: {
    list: () => request<User[]>('/members'),
    create: (data: { email: string; name: string; password: string; role: string; department: string; title: string }) =>
      request<void>('/members', { method: 'POST', body: JSON.stringify(data) }),
    update: (id: string, data: Partial<User>) =>
      request<void>(`/members/${id}`, { method: 'PUT', body: JSON.stringify(data) }),
    remove: (id: string) => request<void>(`/members/${id}`, { method: 'DELETE' }),
  },

  admin: {
    stats: () => request<Stats>('/admin/stats'),
    audit: (limit = 50) => request<AuditLog[]>(`/admin/audit?limit=${limit}`),
    gaps: () => request<Gap[]>('/admin/gaps'),
    feedback: () => request<ChatMessage[]>('/admin/feedback'),
    contact: () => request<{ admins: ContactAdmin[] }>('/contacts/admins'),
    sessions: () => request<SessionWithMeta[]>('/admin/sessions'),
    sessionMessages: (sessionId: string) => request<ChatMessage[]>(`/admin/sessions/${sessionId}/messages`),
  },

  bot: {
    list: () => request<BotBinding[]>('/admin/bot/bindings'),
    decide: (email: string, status: string) =>
      request<{ ok: boolean; status: string; email: string }>('/admin/bot/bindings/decide', {
        method: 'POST',
        body: JSON.stringify({ email, status }),
      }),
    unbind: (id: string) => request<void>(`/admin/bot/bindings/${id}`, { method: 'DELETE' }),
  },
}
