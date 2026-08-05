// console.go — Mission Console (ТЗ Shattl Gateway UI Specification,
// 2026-08-05): полноценный shell из браузера. В отличие от терминала
// gmp-server (тот РЕЛЕИТ байты Dashboard<->agent через VPS, т.к. сервер и
// шлюз — разные машины), здесь браузер и PTY — на ОДНОЙ машине: gateway-ui
// сама открывает PTY локально, релея через VPS не нужно вообще. Протокол
// сообщений (TermMsg: data/resize/close, base64) намеренно совпадает с
// gmp-server/internal/wsapi/terminal.go — тот же формат, тот же фронтенд-
// паттерн (xterm.js + ResizeObserver + UTF-8-safe base64), только без relay-
// прослойки.
package main

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var consoleUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// LAN-only инструмент за паролем (requireAuth уже отфильтровал запрос до
	// апгрейда) — Origin-проверка тут не добавляет защиты, которой уже нет.
	CheckOrigin: func(r *http.Request) bool { return true },
}

type consoleMsg struct {
	Type    string `json:"type"` // data | resize | close
	Data    string `json:"data,omitempty"`
	Cols    int    `json:"cols,omitempty"`
	Rows    int    `json:"rows,omitempty"`
	Message string `json:"message,omitempty"`
}

const consoleWriteWait = 10 * time.Second

// handleConsole — GET /ws/console?cols=&rows= (WS-апгрейд). requireAuth в
// main.go уже проверил сессию ДО вызова этого хендлера — здесь этой проверки
// нет намеренно, чтобы не дублировать (в отличие от gmp-server, где терминал
// живёт вне requireAuth из-за редиректа на /login, тут используется
// requireAuth как есть, редирект на /login до апгрейда безопасен).
func (s *server) handleConsole(w http.ResponseWriter, r *http.Request) {
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}

	ws, err := consoleUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer ws.Close()

	var writeMu sync.Mutex
	send := func(msg consoleMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		ws.SetWriteDeadline(time.Now().Add(consoleWriteWait))
		return ws.WriteJSON(msg)
	}

	sess, err := openShellSession(cols, rows, func(data []byte) {
		send(consoleMsg{Type: "data", Data: base64.StdEncoding.EncodeToString(data)})
	})
	if err != nil {
		send(consoleMsg{Type: "close", Message: "не удалось открыть PTY: " + err.Error()})
		return
	}
	defer sess.Close()

	go func() {
		<-sess.Done()
		send(consoleMsg{Type: "close", Message: "сессия завершена"})
		ws.Close()
	}()

	for {
		var msg consoleMsg
		if err := ws.ReadJSON(&msg); err != nil {
			return
		}
		switch msg.Type {
		case "data":
			raw, err := base64.StdEncoding.DecodeString(msg.Data)
			if err == nil {
				sess.Write(raw)
			}
		case "resize":
			if msg.Cols > 0 && msg.Rows > 0 {
				sess.Resize(msg.Cols, msg.Rows)
			}
		case "close":
			return
		}
	}
}
