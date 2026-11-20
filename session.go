package main

// The listening on the speakers: the queue the music player handed over
// (its items carry stream URLs with the profile's media key), played by
// OwnTone. OwnTone holds only a window of it (the current item and a few
// after it): each URL it is given, it probes at once, so a whole album or
// playlist is never added; the window moves on as it plays, here and not
// in the browser, so a closed tab does not stop the music.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

// Window is how many items OwnTone holds: the current one and those after it.
const Window = 5

// refill: fewer items than this after the current one, and the window is topped up.
const refill = 2

// Limits of a snapshot (the browser's queue).
const (
	MaxItems   = 10000
	maxTextLen = 300
	maxURLLen  = 2048
)

// Item is one item of the queue as the browser hands it over (RemoteItem).
type Item struct {
	Key         string  `json:"key"`
	URL         string  `json:"url"`
	Title       string  `json:"title"`
	Artist      string  `json:"artist"`
	Album       string  `json:"album,omitempty"`
	DurationSec float64 `json:"durationSec"`
}

// Snapshot is the queue as the browser has it (RemoteSnapshot).
type Snapshot struct {
	Kind        string  `json:"kind"`
	Items       []Item  `json:"items"`
	Index       int     `json:"index"`
	PositionSec float64 `json:"positionSec"`
	Playing     bool    `json:"playing"`
	Repeat      string  `json:"repeat"` // off, all, one
	Volume      float64 `json:"volume"`
}

// ErrBadSnapshot: a queue the addon does not take.
var ErrBadSnapshot = errors.New("bad queue")

// Check validates s and rewrites its URLs onto streamBase when set (the
// address OwnTone reaches the server at, when the browser's is not).
func (s *Snapshot) Check(streamBase *url.URL) error {
	if len(s.Items) == 0 || len(s.Items) > MaxItems || s.Index < 0 || s.Index >= len(s.Items) {
		return fmt.Errorf("%w: items", ErrBadSnapshot)
	}
	switch s.Repeat {
	case "", "off", "all", "one":
	default:
		return fmt.Errorf("%w: repeat", ErrBadSnapshot)
	}
	if s.PositionSec < 0 || s.PositionSec > 1e7 || s.Volume < 0 || s.Volume > 1 {
		return fmt.Errorf("%w: position", ErrBadSnapshot)
	}
	keys := make(map[string]bool, len(s.Items))
	for i := range s.Items {
		it := &s.Items[i]
		if it.Key == "" || len(it.Key) > 200 || keys[it.Key] {
			return fmt.Errorf("%w: key", ErrBadSnapshot)
		}
		keys[it.Key] = true
		u, err := streamURL(it.URL, streamBase)
		if err != nil {
			return err
		}
		it.URL = u
		it.Title, it.Artist, it.Album = clip(it.Title), clip(it.Artist), clip(it.Album)
	}
	return nil
}

// streamURL accepts only the server's own audio streams with a media key
// (OwnTone fetches nothing else on the addon's word), rewritten onto base.
func streamURL(raw string, base *url.URL) (string, error) {
	if len(raw) > maxURLLen || strings.Contains(raw, ",") {
		return "", fmt.Errorf("%w: url", ErrBadSnapshot)
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawPath != "" {
		return "", fmt.Errorf("%w: url", ErrBadSnapshot)
	}
	if !strings.HasPrefix(u.Path, "/api/tracks/") && !strings.HasPrefix(u.Path, "/api/audio/") || path.Clean(u.Path) != u.Path {
		return "", fmt.Errorf("%w: url", ErrBadSnapshot)
	}
	if u.Query().Get("mk") == "" {
		return "", fmt.Errorf("%w: url without a media key", ErrBadSnapshot)
	}
	if base != nil {
		u.Scheme, u.Host = base.Scheme, base.Host
		u.Path = strings.TrimSuffix(base.Path, "/") + u.Path
	}
	return u.String(), nil
}

func clip(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	if len(s) > maxTextLen {
		s = s[:maxTextLen]
		for !utfStart(s) {
			s = s[:len(s)-1]
		}
	}
	return strings.TrimSpace(s)
}

// utfStart: s does not end inside a UTF-8 sequence.
func utfStart(s string) bool {
	if s == "" {
		return true
	}
	i := len(s) - 1
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	need := 1
	switch b := s[i]; {
	case b >= 0xF0:
		need = 4
	case b >= 0xE0:
		need = 3
	case b >= 0xC0:
		need = 2
	}
	return len(s)-i >= need
}

// Status is what the browser polls: the session and where OwnTone is.
type Status struct {
	// Session is the session's id ("" none): a browser whose session is
	// not this one any more gives the listening back.
	Session string `json:"session"`
	// Owner is the name of the profile whose listening plays.
	Owner string `json:"owner,omitempty"`
	Mine  bool   `json:"mine"`
	// State: play, pause or stop.
	State      string `json:"state"`
	Key        string `json:"key,omitempty"`
	PositionMs int64  `json:"positionMs"`
	DurationMs int64  `json:"durationMs"`
	// Volume is the master volume, 0-100.
	Volume int `json:"volume"`
	// Ended: the queue played to its end.
	Ended bool `json:"ended,omitempty"`
}

type session struct {
	id        string
	ownerUID  string
	ownerName string
	snap      Snapshot
	// queued: OwnTone's item ids in queue order and the snapshot index each plays
	queued []queued
	// cur: the snapshot index playing now
	cur int
	// seekMs: a position to go to at the next play (the queue was loaded paused)
	seekMs int64
	last   Status
	ended  bool
}

type queued struct{ id, index int }

// Speakers is the listening on OwnTone. Its methods take the lock; each
// call goes to OwnTone through ot (nil: none found).
type Speakers struct {
	mu   sync.Mutex
	ot   func() *OwnTone
	sess *session
	// wait between the player's checks after a start (tests make it short)
	poll time.Duration
}

// ErrNoOwnTone: no OwnTone found.
var ErrNoOwnTone = errors.New("OwnTone not found")

// ErrNoSession: nothing plays on the speakers (or another profile's listening does).
var ErrNoSession = errors.New("no session")

func (sp *Speakers) owntone() (*OwnTone, error) {
	if sp.ot == nil {
		return nil, ErrNoOwnTone
	}
	o := sp.ot()
	if o == nil {
		return nil, ErrNoOwnTone
	}
	return o, nil
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Sync takes the browser's queue (AudioRemote.sync): jump plays
// items[index] from positionSec; otherwise the current item plays on and
// only the window after it follows the new queue. A sync of another
// profile takes the speakers over.
func (sp *Speakers) Sync(ctx context.Context, uid, name string, snap Snapshot, jump bool) (string, error) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	o, err := sp.owntone()
	if err != nil {
		return "", err
	}
	s := sp.sess
	if s == nil || s.ownerUID != uid || jump {
		if s == nil || s.ownerUID != uid {
			s = &session{id: newID(), ownerUID: uid, ownerName: name}
		}
		s.snap, s.ended = snap, false
		sp.sess = s
		return s.id, sp.load(ctx, o, s, snap.Index, int64(snap.PositionSec*1000), snap.Playing)
	}
	return s.id, sp.reconcile(ctx, o, s, snap)
}

func repeatMode(r string) string {
	if r == "one" {
		return "single"
	}
	return "off" // "all" wraps the window here: OwnTone's queue is only a window
}

// next is the snapshot index after i (-1: the end).
func (s *session) next(i int) int {
	if i+1 < len(s.snap.Items) {
		return i + 1
	}
	if s.snap.Repeat == "all" {
		return 0
	}
	return -1
}

// add puts the items from index `from` on (n of them at most, wrapping
// with repeat all) at OwnTone's position pos (-1: the end).
func (sp *Speakers) add(ctx context.Context, o *OwnTone, s *session, from, n, pos int) error {
	var idx []int
	for i := from; i >= 0 && len(idx) < n; i = s.next(i) {
		idx = append(idx, i)
		if len(idx) >= len(s.snap.Items) {
			break // the whole queue once
		}
	}
	if len(idx) == 0 {
		return nil
	}
	uris := make([]string, len(idx))
	for k, i := range idx {
		uris[k] = s.snap.Items[i].URL
	}
	got, err := o.AddQueue(ctx, uris, pos)
	if err != nil {
		return err
	}
	added := make([]queued, len(got))
	for k, q := range got {
		it := s.snap.Items[idx[k]]
		added[k] = queued{id: q.ID, index: idx[k]}
		if err := o.TagItem(ctx, q.ID, Tag{Title: it.Title, Artist: it.Artist, Album: it.Album}); err != nil {
			log.Printf("airplay: tag item: %v", err)
		}
	}
	if pos < 0 || pos >= len(s.queued) {
		s.queued = append(s.queued, added...)
	} else {
		s.queued = append(s.queued[:pos], append(added, s.queued[pos:]...)...)
	}
	return nil
}

// load replaces OwnTone's queue with the window from index i, and plays
// it from ms when play (else it waits there for the next play).
func (sp *Speakers) load(ctx context.Context, o *OwnTone, s *session, i int, ms int64, play bool) error {
	s.queued, s.cur, s.seekMs = nil, i, 0
	if err := o.ClearQueue(ctx); err != nil {
		return err
	}
	if err := o.SetModes(ctx, repeatMode(s.snap.Repeat)); err != nil {
		return err
	}
	if err := sp.add(ctx, o, s, i, Window, -1); err != nil {
		return err
	}
	// the speakers keep their own volume: the browser's (often 100 %) would
	// blast them; only a move of the player's volume slider reaches them
	if !play {
		s.seekMs = ms
		return nil
	}
	return sp.start(ctx, o, s, ms)
}

// start plays and, once the first item sounds, goes to ms.
func (sp *Speakers) start(ctx context.Context, o *OwnTone, s *session, ms int64) error {
	if err := o.Play(ctx); err != nil {
		return err
	}
	if ms < 500 {
		return nil
	}
	poll := sp.poll
	if poll == 0 {
		poll = 100 * time.Millisecond
	}
	// OwnTone seeks only once the item plays (it opens the stream first)
	for range 50 {
		p, err := o.Player(ctx)
		if err != nil {
			return err
		}
		if p.State == "play" {
			return o.Seek(ctx, ms)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(poll):
		}
	}
	return errors.New("owntone: the item did not start")
}

// reconcile follows a new queue whose current item is the one playing:
// the items after it are replaced by the new queue's.
func (sp *Speakers) reconcile(ctx context.Context, o *OwnTone, s *session, snap Snapshot) error {
	p, err := o.Player(ctx)
	if err != nil {
		return err
	}
	at := -1
	for k, q := range s.queued {
		if q.id == p.ItemID {
			at = k
			break
		}
	}
	if at < 0 || s.snap.Items[s.queued[at].index].Key != snap.Items[snap.Index].Key {
		// not what the browser has as current: start over where it is
		s.snap = snap
		return sp.load(ctx, o, s, snap.Index, int64(snap.PositionSec*1000), snap.Playing)
	}
	keep := s.queued[at]
	for k, q := range s.queued {
		if k != at {
			if err := o.DeleteItem(ctx, q.id); err != nil {
				return err
			}
		}
	}
	s.snap = snap
	s.cur = snap.Index
	s.queued = []queued{{id: keep.id, index: snap.Index}}
	if err := o.SetModes(ctx, repeatMode(snap.Repeat)); err != nil {
		return err
	}
	return sp.add(ctx, o, s, s.next(snap.Index), Window-1, -1)
}

// Refresh asks OwnTone where it is: the current item follows OwnTone's,
// the window is topped up, played items leave it. Nothing to do without
// a session.
func (sp *Speakers) Refresh(ctx context.Context) {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	if s := sp.sess; s != nil {
		if o, err := sp.owntone(); err == nil {
			if err := sp.refresh(ctx, o, s); err != nil {
				log.Printf("airplay: refresh: %v", err)
			}
		}
	}
}

func (sp *Speakers) refresh(ctx context.Context, o *OwnTone, s *session) error {
	p, err := o.Player(ctx)
	if err != nil {
		return err
	}
	at := -1
	for k, q := range s.queued {
		if q.id == p.ItemID {
			at = k
			break
		}
	}
	st := Status{Session: s.id, Owner: s.ownerName, State: p.State, Volume: p.Volume, PositionMs: p.ProgressMs, DurationMs: p.LengthMs}
	if at >= 0 {
		s.cur = s.queued[at].index
		// played items leave OwnTone's queue
		for _, q := range s.queued[:at] {
			if err := o.DeleteItem(ctx, q.id); err != nil {
				return err
			}
		}
		s.queued = s.queued[at:]
		if len(s.queued)-1 < refill {
			last := s.queued[len(s.queued)-1].index
			if n := s.next(last); n >= 0 {
				if err := sp.add(ctx, o, s, n, Window-len(s.queued), -1); err != nil {
					return err
				}
			}
		}
	}
	if p.State == "stop" && s.seekMs == 0 && s.last.State == "play" {
		// it played to the end of the window and found nothing after it
		s.ended = true
	}
	if s.cur >= 0 && s.cur < len(s.snap.Items) {
		st.Key = s.snap.Items[s.cur].Key
	}
	if p.State != "play" && s.seekMs > 0 {
		st.PositionMs = s.seekMs
	}
	st.Ended = s.ended
	s.last = st
	return nil
}

// Status is the session as uid sees it (refreshed first).
func (sp *Speakers) Status(ctx context.Context, uid string) Status {
	sp.Refresh(ctx)
	sp.mu.Lock()
	defer sp.mu.Unlock()
	s := sp.sess
	if s == nil {
		return Status{State: "stop"}
	}
	st := s.last
	st.Session, st.Owner, st.Mine = s.id, s.ownerName, s.ownerUID == uid
	return st
}

// mine runs fn on uid's session (ErrNoSession when it is not uid's).
func (sp *Speakers) mine(ctx context.Context, uid, id string, fn func(o *OwnTone, s *session) error) error {
	sp.mu.Lock()
	defer sp.mu.Unlock()
	s := sp.sess
	if s == nil || s.ownerUID != uid || (id != "" && s.id != id) {
		return ErrNoSession
	}
	o, err := sp.owntone()
	if err != nil {
		return err
	}
	return fn(o, s)
}

// Play plays on (from the position a paused load waits at).
func (sp *Speakers) Play(ctx context.Context, uid, id string) error {
	return sp.mine(ctx, uid, id, func(o *OwnTone, s *session) error {
		ms := s.seekMs
		s.seekMs = 0
		if s.ended {
			// the end was reached: the browser's current item again
			s.ended = false
			return sp.load(ctx, o, s, s.cur, 0, true)
		}
		return sp.start(ctx, o, s, ms)
	})
}

func (sp *Speakers) Pause(ctx context.Context, uid, id string) error {
	return sp.mine(ctx, uid, id, func(o *OwnTone, s *session) error { return o.Pause(ctx) })
}

// Seek goes to ms into the current item.
func (sp *Speakers) Seek(ctx context.Context, uid, id string, ms int64) error {
	return sp.mine(ctx, uid, id, func(o *OwnTone, s *session) error {
		p, err := o.Player(ctx)
		if err != nil {
			return err
		}
		if p.State != "play" && p.State != "pause" {
			s.seekMs = ms
			return nil
		}
		return o.Seek(ctx, ms)
	})
}

// Volume sets the master volume (0-100).
func (sp *Speakers) Volume(ctx context.Context, uid, id string, pct int) error {
	return sp.mine(ctx, uid, id, func(o *OwnTone, s *session) error { return o.SetVolume(ctx, pct) })
}

// Stop ends uid's session: the speakers fall silent, OwnTone's queue is emptied.
func (sp *Speakers) Stop(ctx context.Context, uid, id string) error {
	return sp.mine(ctx, uid, id, func(o *OwnTone, s *session) error {
		sp.sess = nil
		if err := o.Stop(ctx); err != nil {
			return err
		}
		if err := o.ClearQueue(ctx); err != nil {
			return err
		}
		// the speakers let go (a HomePod leaves its AirPlay session)
		return o.SetOutputs(ctx, nil)
	})
}

// Run refreshes every second while a session is on: the window moves on
// with no browser asking.
func (sp *Speakers) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			sp.Refresh(rctx)
			cancel()
		}
	}
}
