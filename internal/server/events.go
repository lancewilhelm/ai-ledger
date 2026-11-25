package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// eventHub broadcasts coalesced live-update notifications to connected SSE browser tabs.
type eventHub struct {
	mu            sync.Mutex
	subs          map[chan string]struct{}
	debounce      time.Duration
	timer         *time.Timer
	pendingReason string
	closed        bool
}

func newEventHub(debounce time.Duration) *eventHub {
	if debounce <= 0 {
		debounce = 1500 * time.Millisecond
	}
	return &eventHub{
		subs:     make(map[chan string]struct{}),
		debounce: debounce,
	}
}

func (h *eventHub) Subscribe() chan string {
	ch := make(chan string, 8)
	h.mu.Lock()
	if h.closed {
		close(ch)
	} else {
		h.subs[ch] = struct{}{}
	}
	h.mu.Unlock()
	return ch
}

func (h *eventHub) Unsubscribe(ch chan string) {
	h.mu.Lock()
	if _, ok := h.subs[ch]; ok {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

// Notify schedules a coalesced broadcast so bursts of synced files trigger one UI refresh per window.
func (h *eventHub) Notify(reason string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.pendingReason = reason
	if h.timer != nil {
		return
	}
	h.timer = time.AfterFunc(h.debounce, h.flush)
}

func (h *eventHub) flush() {
	h.mu.Lock()
	h.timer = nil
	if h.closed {
		h.mu.Unlock()
		return
	}
	reason := h.pendingReason
	h.pendingReason = ""
	payload, _ := json.Marshal(map[string]any{
		"reason": reason,
		"at":     time.Now().UTC().Format(time.RFC3339),
	})
	msg := string(payload)
	for ch := range h.subs {
		select {
		case ch <- msg:
		default:
			// Drop if client buffer is full so slow tabs never block ingest/parse.
		}
	}
	h.mu.Unlock()
}

func (h *eventHub) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return
	}
	h.closed = true
	if h.timer != nil {
		h.timer.Stop()
		h.timer = nil
	}
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
	h.mu.Unlock()
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Send initial comment so proxies & EventSource mark the stream open immediately.
	_, _ = fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ch := s.events.Subscribe()
	defer s.events.Unsubscribe(ch)

	keepalive := time.NewTicker(25 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, err := fmt.Fprintf(w, "event: update\ndata: %s\n\n", msg); err != nil {
				return
			}
			flusher.Flush()
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
