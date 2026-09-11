import { useQuery } from '@tanstack/react-query'
import { api, ApiError } from './api'
import type { Me, Role } from './types'

/** Текущий пользователь. 401 — не залогинен (кладём это в data, не в error). */
export function useMe() {
  return useQuery<Me>({
    queryKey: ['auth', 'me'],
    queryFn: async () => {
      try {
        return await api.authMe()
      } catch (e) {
        if (e instanceof ApiError && e.status === 401) {
          return { email: '', role: 'anonymous' as Role, enabled: true }
        }
        throw e
      }
    },
    staleTime: 60_000,
    retry: 1,
  })
}

/** Что разрешено роли в интерфейсе (сервер проверяет то же самое у себя). */
export const can = {
  ai: (role: Role) => role === 'administrator',
  settings: (role: Role) => role !== 'viewer' && role !== 'anonymous',
  generalSettings: (role: Role) => role === 'administrator',
  purge: (role: Role) => role === 'administrator',
  mutate: (role: Role) => role !== 'viewer' && role !== 'anonymous',
}
