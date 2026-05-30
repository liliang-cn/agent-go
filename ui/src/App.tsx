import { useEffect } from 'react'
import { Routes, Route, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useTranslation } from 'react-i18next'
import { QueryTest } from './pages/QueryTest'
import { Documents } from './pages/Documents'
import { Run } from './pages/Run'
import { Live } from './pages/Live'
import { Tasks } from './pages/Tasks'
import { TaskDetail } from './pages/TaskDetail'
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
    `inline-flex items-center justify-center rounded-md px-3 py-1.5 text-sm font-medium transition-colors ${
      isActive
        ? 'bg-primary text-primary-foreground'
        : 'text-muted-foreground hover:bg-accent hover:text-foreground'
    }`

  return (
    <nav className="flex flex-wrap gap-2" data-testid="app-nav">
      <NavLink to="/" className={linkClass} end data-testid="nav-agent">
        {t('agent')}
      </NavLink>
      <NavLink to="/run" className={linkClass} data-testid="nav-run">
        {t('run')}
      </NavLink>
      <NavLink to="/live" className={linkClass} data-testid="nav-live">
        Live
      </NavLink>
      <NavLink to="/tasks" className={linkClass} data-testid="nav-tasks">
        Tasks
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
  const { i18n } = useTranslation()
  const { data: setup } = useSetup()
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    if (!setup) return
    if (!setup.initialized && location.pathname !== '/setup') {
      navigate('/setup', { replace: true })
    }
  }, [setup, location.pathname, navigate])

  const langButton = (active: boolean) =>
    `rounded-[5px] px-2 py-1 text-xs font-medium transition ${
      active
        ? 'bg-primary text-primary-foreground'
        : 'text-muted-foreground hover:text-foreground'
    }`

  return (
    <div className="min-h-screen" data-testid="app-shell">
      <header
        className="sticky top-0 z-20 border-b bg-background/85 backdrop-blur-sm"
        data-testid="app-header"
      >
        <div className="mx-auto flex max-w-[1440px] flex-col gap-3 px-5 py-3 lg:px-8">
          <div className="flex items-center justify-between gap-4">
            <div className="flex items-center gap-2.5">
              <span className="grid h-7 w-7 place-items-center rounded-md bg-primary font-mono text-sm font-bold text-primary-foreground">
                a
              </span>
              <h1 className="text-[1.05rem] font-bold tracking-tight">AgentGo</h1>
              <span className="rounded-full border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                console
              </span>
            </div>
            <div className="flex items-center gap-0.5 rounded-md border bg-background p-0.5">
              <button
                type="button"
                onClick={() => i18n.changeLanguage('zh')}
                className={langButton(i18n.language === 'zh')}
                data-testid="lang-zh"
              >
                中文
              </button>
              <button
                type="button"
                onClick={() => i18n.changeLanguage('en')}
                className={langButton(i18n.language === 'en')}
                data-testid="lang-en"
              >
                EN
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
          <Route path="/live" element={<Live />} />
          <Route path="/tasks" element={<Tasks />} />
          <Route path="/tasks/:id" element={<TaskDetail />} />
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
