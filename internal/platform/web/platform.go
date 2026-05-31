// Package web provides an HTTP management service for remode sessions.
// It implements core.ChatPlatform by fanning out agent output to connected
// SSE clients via a per-session ring buffer + subscriber registry.
package web

import (
	"context"
	"sync"

	"github.com/pickmoment/remode/internal/core"
)

const ringSize = 200 // maximum recent messages buffered per session

// WebMessage wraps a core.Message with its session-prefix for SSE rendering.
type WebMessage struct {
	Msg    core.Message `json:"msg"`
	Prefix string       `json:"prefix"`
}

// Platform implements core.ChatPlatform.
// Send fans out messages to all SSE subscribers for a chatID.
type Platform struct {
	mu      sync.RWMutex
	subs    map[int64]map[int]chan WebMessage // chatID → subID → channel
	buf     map[int64]*ringBuffer            // chatID → recent messages
	nextSub int                              // monotonic subscriber ID

	chatIDMu   sync.Mutex
	nextChatID int64 // seed via SeedChatID before use
}

// New creates a Platform.
func New() *Platform {
	return &Platform{
		subs: make(map[int64]map[int]chan WebMessage),
		buf:  make(map[int64]*ringBuffer),
	}
}

// SeedChatID seeds the synthetic ChatID counter to max(existing web ChatIDs)+1.
// Must be called after sm.ListAll() returns existing sessions.
func (p *Platform) SeedChatID(sessions []*core.Session) {
	p.chatIDMu.Lock()
	defer p.chatIDMu.Unlock()
	var max int64 = 1 << 40 // base of the web ChatID space
	for _, sess := range sessions {
		if sess.Transport == "web" && sess.ChatID > max {
			max = sess.ChatID
		}
	}
	p.nextChatID = max + 1
}

// NextChatID allocates a unique synthetic ChatID for a new web session.
func (p *Platform) NextChatID() int64 {
	p.chatIDMu.Lock()
	defer p.chatIDMu.Unlock()
	id := p.nextChatID
	p.nextChatID++
	return id
}

// Send implements core.ChatPlatform.
// Appends to the ring buffer and non-blocking fan-out to all subscribers.
func (p *Platform) Send(_ context.Context, chatID int64, msg core.Message, sessionPrefix string) error {
	wm := WebMessage{Msg: msg, Prefix: sessionPrefix}

	p.mu.Lock()
	rb := p.ringFor(chatID)
	rb.push(wm)
	chans := make([]chan WebMessage, 0, len(p.subs[chatID]))
	for _, ch := range p.subs[chatID] {
		chans = append(chans, ch)
	}
	p.mu.Unlock()

	for _, ch := range chans {
		// Non-blocking: drop if the subscriber's channel buffer is full.
		select {
		case ch <- wm:
		default:
		}
	}
	return nil
}

// Subscribe registers a client for chatID. Returns:
//   - id: unique subscriber ID for later Unsubscribe
//   - ch: channel that receives live WebMessages
//   - recent: buffered recent messages (for late-joiner replay)
func (p *Platform) Subscribe(chatID int64) (id int, ch chan WebMessage, recent []WebMessage) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ch = make(chan WebMessage, 64)
	p.nextSub++
	id = p.nextSub

	if p.subs[chatID] == nil {
		p.subs[chatID] = make(map[int]chan WebMessage)
	}
	p.subs[chatID][id] = ch

	rb := p.ringFor(chatID)
	recent = rb.snapshot()
	return id, ch, recent
}

// Unsubscribe removes and closes the subscriber channel.
func (p *Platform) Unsubscribe(chatID int64, id int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if subs, ok := p.subs[chatID]; ok {
		if ch, ok := subs[id]; ok {
			close(ch)
			delete(subs, id)
		}
		if len(subs) == 0 {
			delete(p.subs, chatID)
		}
	}
}

// ringFor returns (or creates) the ring buffer for chatID. Must be called with p.mu held.
func (p *Platform) ringFor(chatID int64) *ringBuffer {
	rb, ok := p.buf[chatID]
	if !ok {
		rb = &ringBuffer{cap: ringSize}
		p.buf[chatID] = rb
	}
	return rb
}

// ── ring buffer ───────────────────────────────────────────────────────────────

type ringBuffer struct {
	mu   sync.Mutex
	data []WebMessage
	cap  int
	head int // index of oldest entry
	size int
}

func (r *ringBuffer) push(wm WebMessage) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size < r.cap {
		r.data = append(r.data, wm)
		r.size++
	} else {
		// Overwrite oldest
		r.data[r.head] = wm
		r.head = (r.head + 1) % r.cap
	}
}

func (r *ringBuffer) snapshot() []WebMessage {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.size == 0 {
		return nil
	}
	out := make([]WebMessage, r.size)
	for i := 0; i < r.size; i++ {
		out[i] = r.data[(r.head+i)%len(r.data)]
	}
	return out
}
