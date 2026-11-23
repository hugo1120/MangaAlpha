package httpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xJogger/fake-komga-115/internal/database"
)

const maxSSEClients = 64

type sseEvent struct {
	Event string
	Data  string
}

type sseHub struct {
	mu      sync.Mutex
	clients map[chan sseEvent]string
}

func newSSEHub() *sseHub {
	return &sseHub{
		clients: make(map[chan sseEvent]string),
	}
}

func (h *sseHub) Publish(eventType string, payload any) {
	if h == nil || eventType == "" {
		return
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	evt := sseEvent{Event: eventType, Data: string(raw)}
	h.mu.Lock()
	for ch := range h.clients {
		select {
		case ch <- evt:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *sseHub) PublishForUser(userID, eventType string, payload any) {
	if h == nil || eventType == "" {
		return
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = database.PrimaryAdminUserID
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	evt := sseEvent{Event: eventType, Data: string(raw)}
	h.mu.Lock()
	for ch, uid := range h.clients {
		if uid != userID {
			continue
		}
		select {
		case ch <- evt:
		default:
		}
	}
	h.mu.Unlock()
}

func (h *sseHub) DisconnectUser(userID string) {
	if h == nil {
		return
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return
	}
	h.mu.Lock()
	for ch, uid := range h.clients {
		if uid == userID {
			delete(h.clients, ch)
			close(ch)
		}
	}
	h.mu.Unlock()
}

func (h *sseHub) subscribe(userID string) (chan sseEvent, func(), bool) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		userID = database.PrimaryAdminUserID
	}
	ch := make(chan sseEvent, 64)
	h.mu.Lock()
	if len(h.clients) >= maxSSEClients {
		h.mu.Unlock()
		return nil, nil, false
	}
	h.clients[ch] = userID
	h.mu.Unlock()
	unsubscribe := func() {
		h.mu.Lock()
		if _, ok := h.clients[ch]; ok {
			delete(h.clients, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, unsubscribe, true
}

func (s *Server) komgaSSEEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "Streaming is not supported by this response writer.")
		return
	}
	userID := database.UserIDFromContext(r.Context())
	ch, unsubscribe, ok := s.sse.subscribe(userID)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "SSE_CAPACITY_EXCEEDED", "Too many concurrent event streams.")
		return
	}
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(": connected\n\n"))
	flusher.Flush()

	ticker := time.NewTicker(25 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case evt, open := <-ch:
			if !open {
				return
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", evt.Event, evt.Data); err != nil {
				return
			}
			flusher.Flush()
		case <-ticker.C:
			if _, err := w.Write([]byte(": keepalive\n\n")); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
