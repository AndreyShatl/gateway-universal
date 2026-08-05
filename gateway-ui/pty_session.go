// pty_session.go — независимая копия gmp-agent/internal/terminal/session.go
// (тот же автор/репо, не сторонний код): реальный PTY-shell для Mission
// Console. Дублирование, а не общий пакет — тот же принцип, что и у
// hostmetrics.go: локальная панель не должна зависеть от другого репозитория.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
)

type shellSession struct {
	cmd  *exec.Cmd
	ptmx *os.File
	done chan struct{}
	once sync.Once
}

// openShellSession запускает интерактивный shell пользователя ($SHELL, иначе
// /bin/bash) в PTY заданного размера. onData вызывается из отдельной
// горутины на каждый кусок вывода — не блокировать её надолго.
func openShellSession(cols, rows int, onData func([]byte)) (*shellSession, error) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/bash"
	}
	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
	if err != nil {
		return nil, fmt.Errorf("console: запуск PTY: %w", err)
	}

	s := &shellSession{cmd: cmd, ptmx: ptmx, done: make(chan struct{})}
	go s.readLoop(onData)
	return s, nil
}

func (s *shellSession) readLoop(onData func([]byte)) {
	defer close(s.done)
	buf := make([]byte, 4096)
	for {
		n, err := s.ptmx.Read(buf)
		if n > 0 && onData != nil {
			data := make([]byte, n)
			copy(data, buf[:n])
			onData(data)
		}
		if err != nil {
			return
		}
	}
}

func (s *shellSession) Done() <-chan struct{} { return s.done }

func (s *shellSession) Write(p []byte) (int, error) {
	return s.ptmx.Write(p)
}

func (s *shellSession) Resize(cols, rows int) error {
	return pty.Setsize(s.ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)})
}

func (s *shellSession) Close() error {
	s.once.Do(func() {
		s.ptmx.Close()
		if s.cmd.Process != nil {
			s.cmd.Process.Kill()
		}
	})
	<-s.done
	s.cmd.Wait()
	return nil
}
