import { useEffect, useRef, useState } from 'react'
import { Maximize2, Minimize2, RotateCw, Trash2 } from 'lucide-react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { TopBar } from '../components/TopBar'
import { withBase } from '../lib/basePath'

// UTF-8-safe base64 — обычные btoa/atob падают на не-ASCII (кириллица в
// шелле) — тот же паттерн, что и в старом web/dashboard.html gmp-server.
function b64EncodeUtf8(str: string) {
  const bytes = new TextEncoder().encode(str)
  let bin = ''
  for (const b of bytes) bin += String.fromCharCode(b)
  return btoa(bin)
}
function b64DecodeToBytes(b64: string) {
  const bin = atob(b64)
  const bytes = new Uint8Array(bin.length)
  for (let i = 0; i < bin.length; i++) bytes[i] = bin.charCodeAt(i)
  return bytes
}

const bootLines = ['Establishing secure uplink…', 'Authenticating gateway…', 'Synchronizing telemetry…', 'Connection established.']

export function ConsolePage() {
  const containerRef = useRef<HTMLDivElement>(null)
  const termRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const wsRef = useRef<WebSocket | null>(null)

  const [status, setStatus] = useState('идёт подключение…')
  const [bootStep, setBootStep] = useState(0)
  const [connected, setConnected] = useState(false)
  const [fullscreen, setFullscreen] = useState(false)
  const [gen, setGen] = useState(0) // инкремент — форсирует переподключение

  useEffect(() => {
    const timers = bootLines.map((_, i) => setTimeout(() => setBootStep(i + 1), i * 350))
    return () => timers.forEach(clearTimeout)
  }, [gen])

  useEffect(() => {
    if (!containerRef.current) return
    containerRef.current.innerHTML = ''
    setBootStep(0)
    setStatus('идёт подключение…')

    const term = new Terminal({ convertEol: true, fontSize: 13, theme: { background: '#0b0d11' } })
    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)
    term.open(containerRef.current)
    fitAddon.fit()
    termRef.current = term
    fitRef.current = fitAddon

    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}${withBase('/ws/console')}?cols=${term.cols}&rows=${term.rows}`)
    wsRef.current = ws

    ws.onopen = () => {
      setConnected(true)
      setStatus('подключено')
      term.focus()
    }
    ws.onerror = () => setStatus('ошибка соединения')
    ws.onclose = () => {
      setConnected(false)
      setStatus('соединение закрыто')
    }
    ws.onmessage = (evt) => {
      const msg = JSON.parse(evt.data)
      if (msg.type === 'data') {
        term.write(b64DecodeToBytes(msg.data))
      } else if (msg.type === 'close') {
        setStatus('сессия закрыта' + (msg.message ? ': ' + msg.message : ''))
      }
    }
    term.onData((data) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'data', data: b64EncodeUtf8(data) }))
      }
    })

    const resizeObserver = new ResizeObserver(() => {
      fitAddon.fit()
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
      }
    })
    resizeObserver.observe(containerRef.current)

    return () => {
      resizeObserver.disconnect()
      try {
        ws.close()
      } catch {
        /* ignore */
      }
      term.dispose()
    }
  }, [gen])

  // Fullscreen меняет layout контейнера — терминалу нужно пересчитать
  // размер уже ПОСЛЕ того, как CSS применился, отдельным эффектом.
  useEffect(() => {
    fitRef.current?.fit()
  }, [fullscreen])

  return (
    <div className={fullscreen ? 'fixed inset-0 z-50 bg-bg p-6' : ''}>
      <div className="mb-(--grid-gap) flex items-center justify-between">
        <TopBar title="Mission Console" subtitle={status} live={connected} />
        <div className="flex gap-2">
          <button
            title="Clear"
            onClick={() => termRef.current?.clear()}
            className="flex h-9 w-9 items-center justify-center rounded-md border border-border text-text-secondary hover:border-border-strong hover:text-text"
          >
            <Trash2 size={15} strokeWidth={1.8} />
          </button>
          <button
            title="Reconnect"
            onClick={() => setGen((g) => g + 1)}
            className="flex h-9 w-9 items-center justify-center rounded-md border border-border text-text-secondary hover:border-border-strong hover:text-text"
          >
            <RotateCw size={15} strokeWidth={1.8} />
          </button>
          <button
            title={fullscreen ? 'Exit fullscreen' : 'Fullscreen'}
            onClick={() => setFullscreen((f) => !f)}
            className="flex h-9 w-9 items-center justify-center rounded-md border border-border text-text-secondary hover:border-border-strong hover:text-text"
          >
            {fullscreen ? <Minimize2 size={15} strokeWidth={1.8} /> : <Maximize2 size={15} strokeWidth={1.8} />}
          </button>
        </div>
      </div>

      {bootStep < bootLines.length && (
        <div className="mb-(--grid-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad) font-mono text-[12px] text-text-secondary">
          {bootLines.slice(0, bootStep).map((line, i) => (
            <div key={i}>{line}</div>
          ))}
        </div>
      )}

      <div
        ref={containerRef}
        className={`rounded-[--card-radius] border border-border bg-[#0b0d11] p-2 ${fullscreen ? 'h-[calc(100vh-160px)]' : 'h-[560px]'}`}
      />
    </div>
  )
}
