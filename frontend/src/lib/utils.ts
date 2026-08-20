export function cn(...classes: (string | undefined | null | false)[]) {
  return classes.filter(Boolean).join(' ')
}

export function formatDate(date: string | Date) {
  return new Intl.DateTimeFormat('zh-CN', {
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
  }).format(new Date(date))
}

export function getToken(): string | null {
  return localStorage.getItem('token')
}

export function setToken(token: string) {
  localStorage.setItem('token', token)
}

export function removeToken() {
  localStorage.removeItem('token')
}

export function isAdminRole(role?: string) {
  return role === 'super_admin' || role === 'knowledge_admin'
}

export function roleLabel(role?: string) {
  switch (role) {
    case 'super_admin':
      return '超级管理员'
    case 'knowledge_admin':
      return '知识管理员'
    default:
      return '普通成员'
  }
}
