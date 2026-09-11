import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter } from 'react-router-dom'
import { App } from './App'
import { setUnauthorizedHandler } from './lib/api'
import { ThemeProvider } from './lib/theme'
import { SyncProvider } from './lib/syncContext'
import { ActivityTypesProvider } from './lib/activityTypes'
import { pick } from './i18n/lang'
import './styles.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      refetchOnWindowFocus: false,
      staleTime: 30_000,
    },
  },
})

// Истекла сессия → любой 401 переводит приложение на экран входа: помечаем
// пользователя анонимным и чистим кэш, чтобы не мигали ошибки «не удалось
// загрузить данные» на старых данных.
setUnauthorizedHandler(() => {
  sessionStorage.setItem('uad_session_expired', '1')
  queryClient.setQueryData(['auth', 'me'], { email: '', role: 'anonymous', enabled: true })
  queryClient.removeQueries({ predicate: (q) => q.queryKey[0] !== 'auth' })
})

const container = document.getElementById('root')
if (!container) throw new Error(pick('Не найден корневой элемент #root', 'Root element #root not found'))

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <ThemeProvider>
          <ActivityTypesProvider>
            <SyncProvider>
              <App />
            </SyncProvider>
          </ActivityTypesProvider>
        </ThemeProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
