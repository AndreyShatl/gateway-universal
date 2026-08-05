import { BrowserRouter, Routes, Route, useLocation } from 'react-router-dom'
import { AnimatePresence, motion } from 'framer-motion'
import { RailNav } from './components/RailNav'
import { Overview } from './pages/Overview'
import { ServicesPage } from './pages/ServicesPage'
import { ConsolePage } from './pages/ConsolePage'
import { DomainsPage } from './pages/DomainsPage'
import { WhitelistPage } from './pages/WhitelistPage'
import { MonitorPage } from './pages/MonitorPage'
import { LogsPage } from './pages/LogsPage'
import { SettingsPage } from './pages/SettingsPage'

function AnimatedRoutes() {
  const location = useLocation()
  return (
    <AnimatePresence mode="wait">
      <motion.div
        key={location.pathname}
        initial={{ opacity: 0, y: 6 }}
        animate={{ opacity: 1, y: 0 }}
        exit={{ opacity: 0, y: -6 }}
        transition={{ duration: 0.22, ease: [0.22, 1, 0.36, 1] }}
      >
        <Routes location={location}>
          <Route path="/" element={<Overview />} />
          <Route path="/services" element={<ServicesPage />} />
          <Route path="/console" element={<ConsolePage />} />
          <Route path="/domains" element={<DomainsPage />} />
          <Route path="/whitelist" element={<WhitelistPage />} />
          <Route path="/monitor" element={<MonitorPage />} />
          <Route path="/logs" element={<LogsPage />} />
          <Route path="/settings" element={<SettingsPage />} />
        </Routes>
      </motion.div>
    </AnimatePresence>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <div className="grid min-h-screen grid-cols-[64px_1fr]">
        <RailNav />
        <main className="max-w-[1180px] px-9 py-7 pb-16">
          <AnimatedRoutes />
        </main>
      </div>
    </BrowserRouter>
  )
}
