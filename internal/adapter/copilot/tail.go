package copilot

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/Costroid/costroid/internal/adapter"
)

// tailInterval is how often the session file is read during a run.
const tailInterval = 50 * time.Millisecond

// tail is the watch's place in the session file.
type tail struct {
	file *os.File
	// partial is a line Copilot CLI has not finished writing.
	partial []byte
	read    sessionFile
	// settled counts the requests the watch has reported.
	settled int
	// stopped says the watch met a line it could not read, and reads no more.
	stopped bool
}

func (t *tail) close() {
	if t.file != nil {
		t.file.Close()
	}
}

// Tail follows the session file the plan named until ctx ends, and reports
// the run's requests each time a usage record settles.
func (s *Session) Tail(ctx context.Context, settled func([]adapter.Request)) {
	var t tail
	defer t.close()
	ticker := time.NewTicker(tailInterval)
	defer ticker.Stop()
	for {
		if requests, gained := s.follow(&t); gained {
			settled(requests)
		}
		select {
		case <-ctx.Done():
			s.follow(&t)
			return
		case <-ticker.C:
		}
	}
}

// follow reads what the session file gained. It reports the run's requests
// so far, and true when a new one settled.
func (s *Session) follow(t *tail) ([]adapter.Request, bool) {
	if s.named == "" || t.stopped {
		return nil, false
	}
	if t.file == nil {
		file, err := os.Open(sessionPath(s.home, s.named))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false
		}
		if err != nil {
			s.stopWatch(t, "the session file could not be opened")
			return nil, false
		}
		t.file = file
		s.mu.Lock()
		s.opened = true
		s.mu.Unlock()
	}
	gained, err := io.ReadAll(t.file)
	if err != nil {
		s.stopWatch(t, "the session file could not be read")
		return nil, false
	}
	t.partial = append(t.partial, gained...)
	for {
		end := bytes.IndexByte(t.partial, '\n')
		if end < 0 {
			break
		}
		line := bytes.TrimSpace(t.partial[:end])
		t.partial = t.partial[end+1:]
		if len(line) == 0 {
			continue
		}
		if err := t.read.read(line); err != nil {
			s.stopWatch(t, "a line of the session file could not be read during the run")
			return nil, false
		}
	}
	requests, _, err := t.read.requests(s.keyed)
	if err != nil {
		s.stopWatch(t, "a usage record could not be read during the run")
		return nil, false
	}
	if len(requests) <= t.settled {
		return requests, false
	}
	t.settled = len(requests)
	return requests, true
}

// stopWatch ends the watch, and the grade says why.
func (s *Session) stopWatch(t *tail, problem string) {
	t.stopped = true
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refuse(problem)
}
