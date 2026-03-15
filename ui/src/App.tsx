import { useEffect } from 'react'
import { Routes, Route, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { QueryTest } from './pages/QueryTest'
import { Documents } from './pages/Documents'
import { Run } from './pages/Run'
import { Chat } from './pages/Chat'
import { Status } from './pages/Status'
import { Skills } from './pages/Skills'
import { MCP } from './pages/MCP'
import { Memory } from './pages/Memory'
import { Agent } from './pages/Agent'
import { Settings } from './pages/Settings'
import { Setup } from './pages/Setup'
import { useSetup } from './hooks/useApi'

function Nav() {
  const { t } = useTranslation()
  const linkClass = ({ isActive }: { isActive: boolean }) =>
    `nav-pill ${
      isActive
        ? 'nav-pill-active'
        : 'nav-pill-idle'
    }`

  return (
    <nav className="flex flex-wrap gap-2" data-testid="app-nav">
      <NavLink to="/" className={linkClass} end data-testid="nav-agent">
        {t('agent')}
      </NavLink>
      <NavLink to="/run" className={linkClass} data-testid="nav-run">
        {t('run')}
      </NavLink>
      <NavLink to="/chat" className={linkClass} data-testid="nav-chat">
        {t('chat')}
      </NavLink>
      <NavLink to="/skills" className={linkClass} data-testid="nav-skills">
        {t('skills')}
      </NavLink>
      <NavLink to="/mcp" className={linkClass} data-testid="nav-mcp">
        {t('mcp')}
      </NavLink>
      <NavLink to="/memory" className={linkClass} data-testid="nav-memory">
        {t('memoryNav')}
      </NavLink>
      <NavLink to="/status" className={linkClass} data-testid="nav-status">
        {t('status')}
      </NavLink>
      <NavLink to="/query" className={linkClass} data-testid="nav-query">
        {t('query')}
      </NavLink>
      <NavLink to="/documents" className={linkClass} data-testid="nav-documents">
        {t('documents')}
      </NavLink>
      <NavLink to="/settings" className={linkClass} data-testid="nav-settings">
        {t('settings')}
      </NavLink>
    </nav>
  )
}

function App() {
  const { t, i18n } = useTranslation()
  const { data: setup } = useSetup()
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (!setup) return
    if (!setup.initialized && location.pathname !== '/setup') {
      navigate('/setup', { replace: true })
    }
  }, [setup, location.pathname, navigate])

  return (
    <div className="min-h-screen app-shell" data-testid="app-shell">
      <div className="app-backdrop" />
      <header className="relative z-10 border-b border-sky-100 bg-white/70 backdrop-blur-sm" data-testid="app-header">
        <div className="mx-auto flex max-w-[1440px] flex-col gap-5 px-5 py-6 lg:px-8">
          <div className="flex flex-col gap-4 sm:flex-row sm:items-center sm:justify-between">
            <h1 className="text-3xl font-semibold tracking-tight text-slate-900 sm:text-4xl">
              {t('agentGoUi')}
            </h1>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => i18n.changeLanguage('zh')}
                className={i18n.language === 'zh' ? 'dashboard-button' : 'dashboard-secondary-button text-sm'}
                data-testid="lang-zh"
              >
                中文
              </button>
              <button
                type="button"
                onClick={() => i18n.changeLanguage('en')}
                className={i18n.language === 'en' ? 'dashboard-button' : 'dashboard-secondary-button text-sm'}
                data-testid="lang-en"
              >
                English
              </button>
            </div>
          </div>
          <Nav />
        </div>
      </header>
      <main className="relative z-10 mx-auto max-w-[1440px] px-5 py-8 lg:px-8" data-testid="app-main">
        <Routes>
          <Route path="/" element={<Agent />} />
          <Route path="/run" element={<Run />} />
          <Route path="/chat" element={<Chat />} />
          <Route path="/skills" element={<Skills />} />
          <Route path="/mcp" element={<MCP />} />
          <Route path="/memory" element={<Memory />} />
          <Route path="/status" element={<Status />} />
          <Route path="/query" element={<QueryTest />} />
          <Route path="/documents" element={<Documents />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="/setup" element={<Setup />} />
        </Routes>
      </main>
    </div>
  )
}

export default App
