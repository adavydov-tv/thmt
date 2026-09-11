import { useState, type FormEvent } from 'react'
import { ErrorState } from './ErrorState'
import { SkeletonLines } from './Skeleton'
import { useDiscoverPerson, useHrdbEmployees, useSavePerson } from '../lib/queries'
import { sourceLabel } from '../lib/chartTheme'
import { useT, type MessageKey } from '../i18n'
import type { DiscoverSystemResult, HrdbEmployee, Person } from '../lib/types'

const EMPTY: Person = {
  key: '',
  display_name: '',
  email: '',
  jira_account_id: '',
  gitlab_username: '',
  slack_user_id: '',
  google_email: '',
}

const FIELDS: { name: keyof Person; label: MessageKey; hint?: MessageKey; required?: boolean }[] = [
  { name: 'key', label: 'addPerson.fieldKey', hint: 'addPerson.fieldKeyHint', required: true },
  { name: 'display_name', label: 'addPerson.fieldName', required: true },
  { name: 'email', label: 'addPerson.fieldEmail' },
  { name: 'jira_account_id', label: 'addPerson.fieldJira' },
  { name: 'gitlab_username', label: 'addPerson.fieldGitlab' },
  { name: 'slack_user_id', label: 'addPerson.fieldSlack' },
  { name: 'google_email', label: 'addPerson.fieldGoogle' },
]

interface AddPersonCardProps {
  /** HRDB сконфигурирован на бэкенде — поиск сотрудников доступен. */
  hrdbEnabled: boolean
  /** Человек в режиме редактирования; null — режим добавления. */
  editing: Person | null
  onCancelEdit: () => void
  onSaved: (saved: Person) => void
}

/**
 * Карточка добавления человека. Основной путь — поиск по HRDB: выбираете
 * сотрудника, аккаунты во всех системах находятся по его e-mail, остаётся
 * проверить и сохранить. Полная форма полей — только для редактирования
 * и ручного режима (когда HRDB выключен или человек нестандартный).
 */
export function AddPersonCard({ hrdbEnabled, editing, onCancelEdit, onSaved }: AddPersonCardProps) {
  const { t } = useT()
  const savePerson = useSavePerson()
  const discover = useDiscoverPerson()

  const [manual, setManual] = useState(false)
  const [draft, setDraft] = useState<Person>(EMPTY)
  const [results, setResults] = useState<DiscoverSystemResult[]>([])
  // Заготовка после выбора сотрудника из HRDB; null — ещё в поиске.
  const [picked, setPicked] = useState<Person | null>(null)
  const [query, setQuery] = useState('')
  const [pendingEmail, setPendingEmail] = useState<string | null>(null)

  const searchMode = hrdbEnabled && !manual && !editing
  const employees = useHrdbEmployees(query, searchMode)

  // Смена редактируемого человека приходит снаружи — синхронизируем форму.
  // (Ключ не в state, чтобы не городить эффект: сравниваем с прошлым значением.)
  const [lastEditKey, setLastEditKey] = useState<string | null>(null)
  if ((editing?.key ?? null) !== lastEditKey) {
    setLastEditKey(editing?.key ?? null)
    setDraft(editing ? { ...EMPTY, ...editing } : EMPTY)
    setResults([])
    setPicked(null)
  }

  const reset = () => {
    setDraft(EMPTY)
    setResults([])
    setPicked(null)
    setManual(false)
    if (editing) onCancelEdit()
  }

  const save = (person: Person) => {
    if (!person.key.trim() || !person.display_name.trim()) return
    savePerson.mutate(person, {
      onSuccess: (saved) => {
        reset()
        setQuery('')
        onSaved(saved)
      },
    })
  }

  // Выбор сотрудника из HRDB: сразу ищем его аккаунты во всех системах.
  const pick = (e: HrdbEmployee) => {
    setPendingEmail(e.email)
    discover.mutate(
      { email: e.email, display_name: e.display_name },
      {
        onSuccess: (res) => {
          setPicked({ ...EMPTY, ...res.person })
          setResults(res.results)
        },
        onSettled: () => setPendingEmail(null),
      },
    )
  }

  // «Найти аккаунты по e-mail» в ручной форме: дозаполняет пустые поля.
  const discoverIntoDraft = () => {
    const email = (draft.email ?? '').trim()
    if (!email) return
    discover.mutate(
      { email, key: draft.key, display_name: draft.display_name },
      {
        onSuccess: (res) => {
          setDraft((d) => ({
            ...d,
            key: d.key || res.person.key,
            display_name: d.display_name || res.person.display_name,
            jira_account_id: d.jira_account_id || res.person.jira_account_id,
            gitlab_username: d.gitlab_username || res.person.gitlab_username,
            slack_user_id: d.slack_user_id || res.person.slack_user_id,
            google_email: d.google_email || res.person.google_email,
          }))
          setResults(res.results)
        },
      },
    )
  }

  const submitForm = (e: FormEvent) => {
    e.preventDefault()
    save(draft)
  }

  return (
    <section className="card chart-card">
      <header className="chart-card-head">
        <div className="chart-card-titles">
          <h2>{editing ? t('addPerson.editingTitle', { key: editing.key }) : t('addPerson.title')}</h2>
          <div className="chart-card-subtitle">
            {searchMode ? t('addPerson.subtitleSearch') : t('addPerson.subtitleManual')}
          </div>
        </div>
        <div className="chart-card-actions">
          {editing && (
            <button type="button" className="btn btn-sm" onClick={reset}>
              {t('addPerson.cancel')}
            </button>
          )}
          {!editing && hrdbEnabled && (
            <button type="button" className="btn btn-sm" onClick={() => { setManual(!manual); setPicked(null); setResults([]) }}>
              {manual ? t('addPerson.backToHrdb') : t('addPerson.fillManually')}
            </button>
          )}
        </div>
      </header>

      {searchMode ? (
        <div className="panel-body stack">
          {picked ? (
            /* --- превью выбранного сотрудника: проверить и сохранить --- */
            <>
              <div className="stack" style={{ gap: 4 }}>
                <strong>{picked.display_name}</strong>
                <span className="muted mono">{picked.email}</span>
              </div>

              <div className="field">
                <label htmlFor="picked-key">{t('addPerson.fieldKey')}</label>
                <input
                  id="picked-key"
                  className="input"
                  value={picked.key}
                  onChange={(e) => setPicked({ ...picked, key: e.target.value })}
                />
                <span className="muted" style={{ fontSize: 12 }}>
                  {t('addPerson.keyHint')}
                </span>
              </div>

              <DiscoverResults results={results} />

              <div className="inline-group">
                <button
                  type="button"
                  className="btn btn-primary"
                  disabled={savePerson.isPending || !picked.key.trim()}
                  onClick={() => save(picked)}
                >
                  {savePerson.isPending ? t('addPerson.saving') : t('addPerson.save')}
                </button>
                <button type="button" className="btn" onClick={() => { setPicked(null); setResults([]) }}>
                  {t('addPerson.backToSearch')}
                </button>
                <button
                  type="button"
                  className="btn"
                  title={t('addPerson.editFieldsHint')}
                  onClick={() => { setDraft(picked); setManual(true) }}
                >
                  {t('addPerson.editFields')}
                </button>
              </div>
              {savePerson.isError && <div className="error-text">{savePerson.error.message}</div>}
            </>
          ) : (
            /* --- поиск по HRDB --- */
            <>
              <div className="field">
                <label htmlFor="hrdb-q">{t('addPerson.employee')}</label>
                <input
                  id="hrdb-q"
                  className="input"
                  type="search"
                  placeholder={t('addPerson.searchPlaceholder')}
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                />
              </div>

              {employees.isError ? (
                <ErrorState error={employees.error} onRetry={() => void employees.refetch()} />
              ) : employees.isLoading ? (
                <SkeletonLines count={5} height={22} />
              ) : !employees.data || employees.data.employees.length === 0 ? (
                <p className="muted">{t('addPerson.searchEmpty')}</p>
              ) : (
                <div className="table-wrap" style={{ maxHeight: 340 }}>
                  <table className="data">
                    <caption className="visually-hidden">{t('addPerson.hrdbCaption')}</caption>
                    <tbody>
                      {employees.data.employees.slice(0, 20).map((e) => (
                        <tr key={e.key}>
                          <th scope="row">{e.display_name}</th>
                          <td className="mono muted">{e.email}</td>
                          <td style={{ textAlign: 'right' }}>
                            <button
                              type="button"
                              className="btn btn-sm"
                              disabled={discover.isPending}
                              onClick={() => pick(e)}
                            >
                              {discover.isPending && pendingEmail === e.email
                                ? t('addPerson.searching')
                                : t('addPerson.pick')}
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
              {discover.isError && <div className="error-text">{discover.error.message}</div>}
            </>
          )}
        </div>
      ) : (
        /* --- полная форма: редактирование и ручной режим --- */
        <form className="panel-body stack" onSubmit={submitForm}>
          {FIELDS.map((f) => (
            <div className="field" key={String(f.name)}>
              <label htmlFor={`person-${String(f.name)}`}>
                {t(f.label)}
                {f.required && <span aria-hidden="true"> *</span>}
              </label>
              <input
                id={`person-${String(f.name)}`}
                className="input"
                value={(draft[f.name] as string) ?? ''}
                required={f.required}
                readOnly={f.name === 'key' && Boolean(editing)}
                onChange={(e) => setDraft((d) => ({ ...d, [f.name]: e.target.value }))}
              />
              {f.hint && <span className="muted" style={{ fontSize: 12 }}>{t(f.hint)}</span>}
            </div>
          ))}

          <div className="inline-group">
            <button type="submit" className="btn btn-primary" disabled={savePerson.isPending}>
              {savePerson.isPending ? t('addPerson.saving') : t('addPerson.save')}
            </button>
            <button
              type="button"
              className="btn"
              disabled={!draft.email?.trim() || discover.isPending}
              title={t('addPerson.discoverHint')}
              onClick={discoverIntoDraft}
            >
              {discover.isPending ? t('addPerson.searching') : t('addPerson.discover')}
            </button>
            {savePerson.isSuccess && !savePerson.isPending && (
              <span className="muted">{t('addPerson.saved')}</span>
            )}
          </div>
          {discover.isError && <div className="error-text">{discover.error.message}</div>}
          <DiscoverResults results={results} />
          {savePerson.isError && <div className="error-text">{savePerson.error.message}</div>}
        </form>
      )}
    </section>
  )
}

const STATUS_KEY: Record<DiscoverSystemResult['status'], MessageKey> = {
  found: 'addPerson.statusFound',
  not_found: 'addPerson.statusNotFound',
  error: 'addPerson.statusError',
  disabled: 'addPerson.statusDisabled',
}

/** Итоги автопоиска по системам. */
export function DiscoverResults({ results }: { results: DiscoverSystemResult[] }) {
  const { t } = useT()
  if (results.length === 0) return null
  return (
    <div className="stack" style={{ gap: 6 }}>
      <span className="field-label">{t('addPerson.bySystems')}</span>
      {results.map((r) => (
        <div className="inline-group" key={r.source} style={{ fontSize: 13 }}>
          <span
            className={`badge badge-status ${
              r.status === 'found' ? 'badge-done' : r.status === 'error' ? 'badge-failed' : ''
            }`}
          >
            {t(STATUS_KEY[r.status])}
          </span>
          <strong>{sourceLabel(r.source)}</strong>
          {r.value && <span className="mono">{r.value}</span>}
          {r.detail && <span className="muted">{r.detail}</span>}
        </div>
      ))}
    </div>
  )
}
