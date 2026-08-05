import { LayoutGrid, Globe, ShieldCheck, Activity, ScrollText, Settings, Boxes, TerminalSquare } from 'lucide-react'
import { NavLink, useLocation } from 'react-router-dom'
import { motion } from 'framer-motion'

const items = [
  { to: '/', icon: LayoutGrid, label: 'Overview' },
  { to: '/services', icon: Boxes, label: 'Services' },
  { to: '/console', icon: TerminalSquare, label: 'Mission Console' },
  { to: '/domains', icon: Globe, label: 'Domains' },
  { to: '/whitelist', icon: ShieldCheck, label: 'Whitelist' },
  { to: '/monitor', icon: Activity, label: 'Monitor' },
  { to: '/logs', icon: ScrollText, label: 'Logs' },
]

function RailLink({ to, end, label, icon: Icon }: { to: string; end?: boolean; label: string; icon: typeof LayoutGrid }) {
  const location = useLocation()
  const isActive = end ? location.pathname === to : location.pathname.startsWith(to)

  return (
    <NavLink to={to} end={end} title={label} className="relative flex h-9 w-9 items-center justify-center rounded-lg">
      {isActive && (
        <motion.div
          layoutId="rail-active"
          className="absolute inset-0 rounded-lg bg-accent/[.12]"
          transition={{ type: 'spring', stiffness: 500, damping: 35 }}
        />
      )}
      <Icon
        size={17}
        strokeWidth={1.6}
        className={`relative transition-colors ${isActive ? 'text-accent' : 'text-text-muted hover:text-text-secondary'}`}
      />
    </NavLink>
  )
}

export function RailNav() {
  return (
    <nav className="flex w-16 flex-col items-center gap-1 border-r border-border py-5">
      <div className="relative mb-7 flex h-[30px] w-[30px] items-center justify-center rounded-lg border border-border-strong">
        <div className="h-2 w-2 rounded-full border border-accent" />
        <div className="absolute -inset-1 rounded-lg border border-accent opacity-35" />
      </div>
      {items.map(({ to, icon, label }) => (
        <RailLink key={to} to={to} end={to === '/'} label={label} icon={icon} />
      ))}
      <div className="mt-auto">
        <RailLink to="/settings" label="Settings" icon={Settings} />
      </div>
    </nav>
  )
}
