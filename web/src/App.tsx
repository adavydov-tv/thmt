import { Route, Routes } from 'react-router-dom'
import { Header } from './components/Header'
import { OnboardingTour, useOnboarding } from './components/OnboardingTour'
import { Overview } from './pages/Overview'
import { SourcePage } from './pages/SourcePage'
import { ComparePage } from './pages/ComparePage'
import { FormatComparePage } from './pages/FormatComparePage'
import { AbusersPage } from './pages/AbusersPage'
import { AIPage } from './pages/AIPage'
import { TeamsPage } from './pages/TeamsPage'
import { ViolationsPage } from './pages/ViolationsPage'
import { DocsPage } from './pages/DocsPage'
import { EventsPage } from './pages/EventsPage'
import { EventDetail } from './pages/EventDetail'
import { SettingsSyncPage, SettingsGeneralPage, SettingsActivityPage, SettingsHolidaysPage, SettingsPurgePage, SettingsUsersPage } from './pages/SettingsPage'
import { EmptyState } from './components/EmptyState'
import { I18nProvider, useLang, useT } from './i18n'
import { LoginPage } from './pages/LoginPage'
import { useMe, can } from './lib/authClient'

function AppInner() {
  const onboarding = useOnboarding()
  const { lang } = useLang()
  const { t } = useT()
  const me = useMe()
  if (me.isLoading) return null
  if (!me.data || me.data.role === 'anonymous') return <LoginPage />
  const role = me.data.role
  return (
    // key={lang}: смена языка перемонтирует дерево — переводятся и надписи,
    // которые берутся из чистых функций (метки источников, справочник правил).
    <div className="app" key={lang}>
      <Header onShowHelp={onboarding.show} />
      {onboarding.open && <OnboardingTour onClose={onboarding.close} />}
      <main className="main">
        <Routes>
          <Route path="/" element={<Overview />} />
          <Route path="/source/:source" element={<SourcePage />} />
          <Route path="/compare" element={<ComparePage />} />
          <Route path="/compare/format" element={<FormatComparePage />} />
          <Route path="/abusers" element={<AbusersPage />} />
          <Route path="/teams" element={<TeamsPage />} />
          <Route path="/violations" element={<ViolationsPage />} />
          <Route path="/ai" element={can.ai(role) ? <AIPage /> : <Overview />} />
          <Route path="/docs" element={<DocsPage />} />
          <Route path="/events" element={<EventsPage />} />
          <Route path="/events/:id" element={<EventDetail />} />
          <Route path="/settings" element={can.settings(role) ? <SettingsSyncPage /> : <Overview />} />
          <Route
            path="/settings/general"
            element={can.generalSettings(role) ? <SettingsGeneralPage /> : <Overview />}
          />
          <Route
            path="/settings/activity"
            element={can.generalSettings(role) ? <SettingsActivityPage /> : <Overview />}
          />
          <Route
            path="/settings/holidays"
            element={can.settings(role) ? <SettingsHolidaysPage /> : <Overview />}
          />
          <Route path="/settings/purge" element={can.purge(role) ? <SettingsPurgePage /> : <Overview />} />
          <Route
            path="/settings/users"
            element={role === 'administrator' ? <SettingsUsersPage /> : <Overview />}
          />
          <Route
            path="*"
            element={
              <EmptyState
                title={t('notFound.title')}
                description={t('notFound.description')}
                showSyncLink={false}
              />
            }
          />
        </Routes>
      </main>
    </div>
  )
}

export function App() {
  return (
    <I18nProvider>
      <AppInner />
    </I18nProvider>
  )
}
