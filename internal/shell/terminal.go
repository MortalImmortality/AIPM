package shell

import (
	"aipm/internal/app"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"unicode/utf8"

	"github.com/charmbracelet/x/term"
)

var errInterrupted = errors.New("输入已中断")
var errTerminalRestore = errors.New("无法恢复终端，请运行 stty sane")

// terminal owns mode changes so interrupt cleanup cannot race another prompt.
type terminal struct {
	input   *os.File
	mu      sync.Mutex
	state   *term.State
	stopped bool
}

func (t *terminal) restore() error {
	if t.state == nil {
		return nil
	}
	err := term.Restore(t.input.Fd(), t.state)
	if err == nil {
		t.state = nil
	}
	return err
}
func (t *terminal) secret() (value string, err error) {
	t.mu.Lock()
	if t.stopped {
		t.mu.Unlock()
		return "", errInterrupted
	}
	state, e := term.MakeRaw(t.input.Fd())
	if e != nil {
		t.mu.Unlock()
		return "", fmt.Errorf("无法隐藏凭据输入")
	}
	t.state = state
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		restoreErr := t.restore()
		t.mu.Unlock()
		if restoreErr != nil {
			value = ""
			err = errTerminalRestore
		}
	}()
	// Read directly without buffering: the ordinary reader must not prefetch secrets.
	var valueBytes []byte
	oversized := false
	defer func() { clear(valueBytes) }()
	for {
		var b [1]byte
		n, e := t.input.Read(b[:])
		if e != nil {
			return "", e
		}
		if n == 0 {
			continue
		}
		switch b[0] {
		case 3:
			return "", errInterrupted
		case 4:
			return "", io.EOF
		case '\r', '\n':
			if oversized {
				return "", fmt.Errorf("凭据过长")
			}
			return string(valueBytes), nil
		case 8, 127:
			if len(valueBytes) > 0 {
				_, size := utf8.DecodeLastRune(valueBytes)
				valueBytes = valueBytes[:len(valueBytes)-size]
			}
		case 21:
			clear(valueBytes)
			valueBytes = valueBytes[:0]
		default:
			if b[0] < 32 {
				continue
			}
			if len(valueBytes) >= 8192 || oversized {
				oversized = true
				continue
			}
			valueBytes = append(valueBytes, b[0])
		}
	}
}

// RunTerminal keeps normal shell echo and history except during a secret prompt.
func RunTerminal(a *app.App, in *os.File, out io.Writer) error {
	t := &terminal{input: in}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	finished := make(chan struct{})
	defer signal.Stop(signals)
	defer close(finished)
	defer func() { t.mu.Lock(); _ = t.restore(); t.mu.Unlock() }()
	go func() {
		select {
		case sig := <-signals:
			t.mu.Lock()
			t.stopped = true
			err := t.restore()
			t.mu.Unlock()
			// A blocked terminal read cannot be cancelled by a context. Restore first,
			// then terminate rather than resume menus with an abandoned stdin reader.
			if err != nil {
				fmt.Fprintln(os.Stderr, "无法恢复终端，请运行 stty sane")
			}
			code := 130
			if sig == syscall.SIGTERM {
				code = 143
			}
			os.Exit(code)
		case <-finished:
		}
	}()
	err := Run(a, in, out, t.secret)
	if errors.Is(err, errInterrupted) {
		return nil
	}
	return err
}
