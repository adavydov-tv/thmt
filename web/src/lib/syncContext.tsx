import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api, ApiError } from './api'
import { keys, useInvalidateStats, useStartSync, useSyncRun } from './queries'
import type { SyncRequest, SyncRun } from './types'

/** Ключи в localStorage: активный прогон переживает перезагрузку страницы. */
const RUN_KEY = 'sync-active-run-id'
const DISMISSED_KEY = 'sync-dismissed-run-id'

function readKey(key: string): string | null {
  try {
    return localStorage.getItem(key)
  } catch {
    return null
  }
}

function writeKey(key: string, value: string | null) {
  try {
    if (value) localStorage.setItem(key, value)
    else localStorage.removeItem(key)
  } catch {
    // localStorage недоступен — прогон не переживёт перезагрузку
  }
}

interface SyncContextValue {
  activeRun: SyncRun | undefined
  activeRunId: string | null
  isStarting: boolean
  isCancelling: boolean
  error: Error | null
  start: (req: SyncRequest) => void
  /** Остановить активный прогон (частичные данные сохраняются). */
  cancel: () => void
  dismiss: () => void
}

const SyncContext = createContext<SyncContextValue>({
  activeRun: undefined,
  activeRunId: null,
  isStarting: false,
  isCancelling: false,
  error: null,
  start: () => {},
  cancel: () => {},
  dismiss: () => {},
})

/** Один активный прогон на всё приложение: поллинг /api/sync/{id} раз в 2 секунды. */
export function SyncProvider({ children }: { children: ReactNode }) {
  const [activeRunId, setActiveRunIdState] = useState<string | null>(() => readKey(RUN_KEY))
  const [dismissedRunId, setDismissedRunId] = useState<string | null>(() => readKey(DISMISSED_KEY))
  const startMutation = useStartSync()
  const runQuery = useSyncRun(activeRunId)
  const invalidateStats = useInvalidateStats()

  const setActiveRunId = useCallback((id: string | null) => {
    writeKey(RUN_KEY, id)
    setActiveRunIdState(id)
  }, [])

  // Сбор идёт на бэкенде и перезагрузку страницы не замечает. Если id прогона
  // не сохранился (другая вкладка, чистый localStorage) — подхватываем
  // незавершённый прогон из истории.
  const recovery = useQuery<SyncRun[]>({
    queryKey: ['sync', 'recovery'] as const,
    queryFn: () => api.syncRuns('', 10),
    enabled: !activeRunId,
    staleTime: Infinity,
    retry: false,
  })

  useEffect(() => {
    if (activeRunId || !recovery.data) return
    const running = recovery.data.find((r) => r.status === 'pending' || r.status === 'running')
    if (running && running.id !== dismissedRunId) {
      setActiveRunId(running.id)
    }
  }, [recovery.data, activeRunId, dismissedRunId, setActiveRunId])

  // Восстановленный id может указывать на уже удалённый прогон (история
  // почищена) — забываем его, иначе ошибка 404 будет возвращаться после
  // каждой перезагрузки.
  useEffect(() => {
    if (runQuery.error instanceof ApiError && runQuery.error.status === 404) {
      setActiveRunId(null)
    }
  }, [runQuery.error, setActiveRunId])

  const status = runQuery.data?.status

  useEffect(() => {
    if (status === 'done' || status === 'partial' || status === 'failed') {
      invalidateStats()
      // Завершённый прогон не восстанавливаем после перезагрузки: плашка с
      // итогом остаётся до dismiss только в текущей вкладке.
      writeKey(RUN_KEY, null)
    }
    // invalidateStats стабилен по смыслу; зависимость только от статуса прогона
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [status, activeRunId])

  const start = useCallback(
    (req: SyncRequest) => {
      startMutation.mutate(req, {
        onSuccess: (run) => setActiveRunId(run.id),
      })
    },
    [startMutation, setActiveRunId],
  )

  // Остановка активного прогона: бэкенд прерывает коллекторы и сохраняет
  // частичные данные; финальный статус кладём в кэш, чтобы плашка обновилась
  // сразу, не дожидаясь следующего опроса.
  const qc = useQueryClient()
  const cancelMutation = useMutation<SyncRun, Error, string>({
    mutationFn: api.cancelSync,
    onSuccess: (run) => {
      qc.setQueryData(keys.syncRun(run.id), run)
      void qc.invalidateQueries({ queryKey: keys.syncRun(run.id) })
    },
  })
  const cancel = useCallback(() => {
    if (activeRunId && !cancelMutation.isPending) {
      cancelMutation.mutate(activeRunId)
    }
  }, [activeRunId, cancelMutation])

  const dismiss = useCallback(() => {
    // Запоминаем, что этот прогон скрыт вручную, иначе восстановление
    // тут же вернёт его на экран.
    writeKey(DISMISSED_KEY, activeRunId)
    setDismissedRunId(activeRunId)
    setActiveRunId(null)
  }, [activeRunId, setActiveRunId])

  const value = useMemo<SyncContextValue>(
    () => ({
      activeRun: runQuery.data,
      activeRunId,
      isStarting: startMutation.isPending,
      isCancelling: cancelMutation.isPending,
      error: (startMutation.error as Error | null) ?? (runQuery.error as Error | null) ?? null,
      start,
      cancel,
      dismiss,
    }),
    [runQuery.data, runQuery.error, activeRunId, startMutation.isPending, startMutation.error,
     cancelMutation.isPending, start, cancel, dismiss],
  )

  return <SyncContext.Provider value={value}>{children}</SyncContext.Provider>
}

export function useSync(): SyncContextValue {
  return useContext(SyncContext)
}
