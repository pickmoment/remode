package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// heartbeatInterval is how often a keep-alive comment is sent to prevent
// proxy / load-balancer connection timeouts.
const heartbeatInterval = 20 * time.Second

// handleStream serves GET /api/sessions/{name}/stream as an SSE stream.
// The session is identified by name (path value), resolved to ChatID, then
// subscribed to the Platform's fan-out.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	sess := s.sm.Get(name)
	if sess == nil {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	chatID := sess.ChatID

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	subID, ch, recent := s.platform.Subscribe(chatID)
	defer s.platform.Unsubscribe(chatID, subID)

	// Replay recent buffered messages to catch up late-joining clients.
	for _, wm := range recent {
		writeSSEEvent(w, wm)
	}
	flusher.Flush()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		case wm, ok := <-ch:
			if !ok {
				return
			}
			writeSSEEvent(w, wm)
			flusher.Flush()
		}
	}
}

func writeSSEEvent(w http.ResponseWriter, wm WebMessage) {
	data, err := json.Marshal(wm)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "data: %s\n\n", data)
}
