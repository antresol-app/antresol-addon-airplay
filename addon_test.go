package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/antresol-app/antresol/addonkit"
)

// fakeOwnTone answers the part of OwnTone's JSON API the addon uses.
type fakeOwnTone struct {
	mu      sync.Mutex
	nextID  int
	queue   []fakeItem
	state   string
	itemID  int
	progMs  int64
	volume  int
	repeat  string
	outputs []Output
	calls   []string
	probes  int
}

type fakeItem struct {
	ID                   int
	URI                  string
	Title, Artist, Album string
}

func newFake() *fakeOwnTone {
	return &fakeOwnTone{nextID: 1, state: "stop", volume: 50, outputs: []Output{
		{ID: "1", Name: "Living room", Type: "AirPlay 2", Volume: 40},
		{ID: "2", Name: "Shelf", Type: "AirPlay 1", Volume: 30, Selected: true},
		{ID: "0", Name: "Computer", Type: "ALSA"},
		{ID: "3", Name: "TV", Type: "Chromecast", NeedsAuthKey: true},
	}}
}

func (f *fakeOwnTone) uris() []string {
	var out []string
	for _, it := range f.queue {
		out = append(out, it.URI)
	}
	return out
}

func (f *fakeOwnTone) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	q := r.URL.Query()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	js := func(v any) { w.Header().Set("Content-Type", "application/json"); json.NewEncoder(w).Encode(v) }
	switch p := r.URL.Path; {
	case p == "/api/config":
		js(map[string]any{"version": "29.3"})
	case p == "/api/outputs" && r.Method == http.MethodGet:
		js(map[string]any{"outputs": f.outputs})
	case p == "/api/outputs/set" && r.Method == http.MethodPut:
		var body struct{ Outputs []string }
		json.NewDecoder(r.Body).Decode(&body)
		for i := range f.outputs {
			f.outputs[i].Selected = slices.Contains(body.Outputs, f.outputs[i].ID)
		}
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/api/outputs/") && r.Method == http.MethodPut:
		var c OutputChange
		json.NewDecoder(r.Body).Decode(&c)
		for i := range f.outputs {
			if f.outputs[i].ID == strings.TrimPrefix(p, "/api/outputs/") {
				if c.Selected != nil {
					f.outputs[i].Selected = *c.Selected
				}
				if c.Volume != nil {
					f.outputs[i].Volume = *c.Volume
				}
			}
		}
		w.WriteHeader(204)
	case p == "/api/player":
		js(Player{State: f.state, ItemID: f.itemID, ProgressMs: f.progMs, Volume: f.volume, LengthMs: 200000, Repeat: f.repeat})
	case p == "/api/player/play":
		if len(f.queue) == 0 {
			w.WriteHeader(500)
			return
		}
		if f.state == "stop" {
			f.itemID = f.queue[0].ID
		}
		f.state = "play"
		w.WriteHeader(204)
	case p == "/api/player/pause":
		f.state = "pause"
		w.WriteHeader(204)
	case p == "/api/player/stop":
		f.state = "stop"
		w.WriteHeader(204)
	case p == "/api/player/seek":
		f.progMs, _ = strconv.ParseInt(q.Get("position_ms"), 10, 64)
		w.WriteHeader(204)
	case p == "/api/player/volume":
		f.volume, _ = strconv.Atoi(q.Get("volume"))
		w.WriteHeader(204)
	case p == "/api/player/repeat":
		f.repeat = q.Get("state")
		w.WriteHeader(204)
	case p == "/api/player/shuffle" || p == "/api/player/consume":
		w.WriteHeader(204)
	case p == "/api/queue/clear":
		f.queue, f.state, f.itemID = nil, "stop", 0
		w.WriteHeader(204)
	case p == "/api/queue/items/add":
		var added []QueueItem
		var items []fakeItem
		for _, u := range strings.Split(q.Get("uris"), ",") {
			f.probes++
			it := fakeItem{ID: f.nextID, URI: u}
			f.nextID++
			items = append(items, it)
			added = append(added, QueueItem{ID: it.ID, URI: u})
		}
		pos := len(f.queue)
		if s := q.Get("position"); s != "" {
			pos, _ = strconv.Atoi(s)
		}
		f.queue = append(f.queue[:pos], append(items, f.queue[pos:]...)...)
		js(map[string]any{"count": len(added), "items": added})
	case strings.HasPrefix(p, "/api/queue/items/") && r.Method == http.MethodPut:
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/api/queue/items/"))
		for i := range f.queue {
			if f.queue[i].ID == id {
				f.queue[i].Title, f.queue[i].Artist, f.queue[i].Album = q.Get("title"), q.Get("artist"), q.Get("album")
			}
		}
		w.WriteHeader(204)
	case strings.HasPrefix(p, "/api/queue/items/") && r.Method == http.MethodDelete:
		id, _ := strconv.Atoi(strings.TrimPrefix(p, "/api/queue/items/"))
		for i := range f.queue {
			if f.queue[i].ID == id {
				f.queue = append(f.queue[:i], f.queue[i+1:]...)
				break
			}
		}
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

// advance moves the fake's player to the next item (the current one ended).
func (f *fakeOwnTone) advance() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, it := range f.queue {
		if it.ID == f.itemID {
			if i+1 < len(f.queue) {
				f.itemID, f.progMs = f.queue[i+1].ID, 0
			} else {
				f.state = "stop"
			}
			return
		}
	}
}

func setup(t *testing.T) (*fakeOwnTone, *addon, *httptest.Server) {
	t.Helper()
	f := newFake()
	ot := httptest.NewServer(f)
	t.Cleanup(ot.Close)
	fd := &Finder{Override: ot.URL}
	if err := fd.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	a := &addon{finder: fd, helper: addonkit.Helper{Name: "owntone", Image: "owntone/owntone:29.3", Port: 3689, Health: "/api/config"},
		speakers: &Speakers{ot: fd.OwnTone, poll: time.Millisecond}}
	srv := httptest.NewServer(newHandler(a, readySet()))
	t.Cleanup(srv.Close)
	return f, a, srv
}

func readySet() *addonkit.Ready { r := &addonkit.Ready{}; r.Set(); return r }

func items(n int) []Item {
	out := make([]Item, n)
	for i := range out {
		out[i] = Item{Key: fmt.Sprintf("t%d", i), URL: fmt.Sprintf("http://lan:8420/api/tracks/%d/stream?mk=K", i), Title: fmt.Sprintf("Song %d", i), Artist: "A", DurationSec: 200}
	}
	return out
}

func call(t *testing.T, srv *httptest.Server, uid, method, path string, body any) (int, []byte) {
	t.Helper()
	var rd *strings.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = strings.NewReader(string(b))
	} else {
		rd = strings.NewReader("")
	}
	req, _ := http.NewRequest(method, srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if uid != "" {
		req.Header.Set("X-Antresol-User", fmt.Sprintf(`{"id":1,"uid":%q,"name":"P-%s","isAdmin":true,"isKid":false,"lang":"ru"}`, uid, uid))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf strings.Builder
	b := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(b)
		buf.Write(b[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, []byte(buf.String())
}

func TestCheckRefusesForeignURLs(t *testing.T) {
	for _, u := range []string{
		"http://lan/api/tracks/1/stream",              // no media key
		"http://lan/api/images/x?mk=K",                // not an audio stream
		"file:///etc/passwd?mk=K",                     // not http
		"http://lan/api/tracks/1/stream?mk=K,http://x", // a comma (OwnTone's separator)
		"http://user:pw@lan/api/tracks/1/stream?mk=K",
		"http://lan/api/tracks/../../x?mk=K",
	} {
		s := Snapshot{Items: []Item{{Key: "a", URL: u}}}
		if err := s.Check(nil); err == nil {
			t.Errorf("%s: taken", u)
		}
	}
	s := Snapshot{Items: []Item{{Key: "a", URL: "http://localhost:5173/api/audio/7?mk=K", Title: "x\x00y"}}}
	base, _ := url.Parse("http://192.0.2.1:8420/root")
	if err := s.Check(base); err != nil {
		t.Fatal(err)
	}
	if got := s.Items[0].URL; got != "http://192.0.2.1:8420/root/api/audio/7?mk=K" {
		t.Errorf("rewritten URL %q", got)
	}
	if s.Items[0].Title != "x y" {
		t.Errorf("title %q", s.Items[0].Title)
	}
	dup := Snapshot{Items: []Item{{Key: "a", URL: "http://l/api/audio/1?mk=K"}, {Key: "a", URL: "http://l/api/audio/2?mk=K"}}}
	if dup.Check(nil) == nil {
		t.Error("a repeated key taken")
	}
}

func TestStateListsSpeakersOnly(t *testing.T) {
	_, _, srv := setup(t)
	code, body := call(t, srv, "u1", "GET", "/api/state", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var st State
	json.Unmarshal(body, &st)
	if !st.Ready || len(st.Outputs) != 3 {
		t.Fatalf("state %+v", st)
	}
	names := []string{}
	for _, o := range st.Outputs {
		names = append(names, o.Name+":"+o.Kind)
	}
	if strings.Join(names, ",") != "Living room:airplay,Shelf:airplay,TV:chromecast" {
		t.Errorf("outputs %v", names)
	}
	if !st.Outputs[2].NeedsPin {
		t.Error("a device asking a PIN not marked")
	}
	if code, _ := call(t, srv, "", "GET", "/api/state", nil); code != 401 {
		t.Errorf("no profile: %d", code)
	}
}

func TestOutputsAndPin(t *testing.T) {
	f, _, srv := setup(t)
	if code, b := call(t, srv, "u1", "PUT", "/api/outputs/1", map[string]any{"selected": true, "volume": 20}); code != 204 {
		t.Fatalf("%d %s", code, b)
	}
	if !f.outputs[0].Selected || f.outputs[0].Volume != 20 {
		t.Errorf("output %+v", f.outputs[0])
	}
	if code, _ := call(t, srv, "u1", "PUT", "/api/outputs/1", map[string]any{"volume": 101}); code != 400 {
		t.Errorf("volume 101: %d", code)
	}
	if code, _ := call(t, srv, "u1", "POST", "/api/outputs/3/pin", map[string]any{"pin": "12a4"}); code != 400 {
		t.Errorf("bad pin: %d", code)
	}
	if code, _ := call(t, srv, "u1", "POST", "/api/outputs/3/pin", map[string]any{"pin": "1234"}); code != 204 {
		t.Errorf("pin: %d", code)
	}
	if code, _ := call(t, srv, "u1", "PUT", "/api/outputs/..%2f", map[string]any{"selected": true}); code == 204 {
		t.Error("a bad output id taken")
	}
	// exactly one speaker: the others go off
	if code, _ := call(t, srv, "u1", "PUT", "/api/outputs", map[string]any{"ids": []string{"3"}}); code != 204 {
		t.Errorf("set: %d", code)
	}
	if f.outputs[0].Selected || f.outputs[1].Selected || !f.outputs[3].Selected {
		t.Errorf("after set %+v", f.outputs)
	}
	if code, _ := call(t, srv, "u1", "PUT", "/api/outputs", map[string]any{"ids": []string{"a b"}}); code != 400 {
		t.Errorf("bad id in set: %d", code)
	}
}

func TestSessionWindowFollowsPlayback(t *testing.T) {
	f, a, srv := setup(t)
	snap := Snapshot{Kind: "music", Items: items(8), Index: 2, PositionSec: 30, Playing: true, Repeat: "off", Volume: 0.4}
	code, b := call(t, srv, "u1", "POST", "/api/session", map[string]any{"snapshot": snap, "jump": true})
	if code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	var r struct{ Session string }
	json.Unmarshal(b, &r)
	if r.Session == "" {
		t.Fatal("no session id")
	}
	// the window: items 2..6, tagged, playing from 30 s at 40 %
	if got := f.uris(); len(got) != Window || !strings.Contains(got[0], "/tracks/2/") || !strings.Contains(got[4], "/tracks/6/") {
		t.Fatalf("queue %v", got)
	}
	if f.queue[0].Title != "Song 2" || f.state != "play" || f.progMs != 30000 || f.volume != 50 {
		t.Fatalf("player: %+v state %s pos %d vol %d", f.queue[0], f.state, f.progMs, f.volume)
	}
	// two items end: played ones leave, the window is topped up to the end (7)
	f.advance()
	f.advance()
	f.advance()
	ctx := context.Background()
	st := a.speakers.Status(ctx, "u1")
	if st.Key != "t5" || !st.Mine || st.Session != r.Session {
		t.Fatalf("status %+v", st)
	}
	if got := f.uris(); !strings.Contains(got[0], "/tracks/5/") || !strings.Contains(got[len(got)-1], "/tracks/7/") {
		t.Fatalf("queue after %v", got)
	}
	// the queue changes after the current item: it plays on, the rest follows
	cur := f.itemID
	snap2 := Snapshot{Kind: "music", Items: append(items(6), Item{Key: "x", URL: "http://lan:8420/api/tracks/99/stream?mk=K"}), Index: 5, Playing: true}
	if code, b := call(t, srv, "u1", "POST", "/api/session", map[string]any{"snapshot": snap2, "jump": false}); code != 200 {
		t.Fatalf("%d %s", code, b)
	}
	if f.itemID != cur || len(f.queue) != 2 || f.queue[0].ID != cur || !strings.Contains(f.queue[1].URI, "/tracks/99/") {
		t.Fatalf("reconciled queue %+v (current %d)", f.queue, cur)
	}
	// pause, seek, volume, then the end of the queue
	for _, c := range []struct {
		path string
		body map[string]any
	}{{"pause", nil}, {"seek", map[string]any{"ms": 5000}}, {"volume", map[string]any{"volume": 10}}} {
		body := map[string]any{"session": r.Session}
		for k, v := range c.body {
			body[k] = v
		}
		if code, b := call(t, srv, "u1", "POST", "/api/session/"+c.path, body); code != 204 {
			t.Fatalf("%s: %d %s", c.path, code, b)
		}
	}
	if f.state != "pause" || f.progMs != 5000 || f.volume != 10 {
		t.Fatalf("after controls: %s %d %d", f.state, f.progMs, f.volume)
	}
	call(t, srv, "u1", "POST", "/api/session/play", map[string]any{"session": r.Session})
	a.speakers.Status(ctx, "u1")
	f.advance()
	f.advance()
	if st := a.speakers.Status(ctx, "u1"); !st.Ended {
		t.Errorf("end not seen: %+v", st)
	}
}

func TestRepeatAllWraps(t *testing.T) {
	f, a, _ := setup(t)
	ctx := context.Background()
	if _, err := a.speakers.Sync(ctx, "u1", "P", Snapshot{Items: items(3), Index: 1, Playing: true, Repeat: "all"}, true); err != nil {
		t.Fatal(err)
	}
	if got := f.uris(); len(got) != 3 || !strings.Contains(got[2], "/tracks/0/") {
		t.Fatalf("window %v", got)
	}
	f.advance()
	f.advance()
	a.speakers.Refresh(ctx)
	if got := f.uris(); len(got) < 2 || !strings.Contains(got[1], "/tracks/1/") {
		t.Fatalf("not wrapped: %v", got)
	}
	// repeat one: OwnTone's own single repeat
	if _, err := a.speakers.Sync(ctx, "u1", "P", Snapshot{Items: items(3), Index: 0, Playing: true, Repeat: "one"}, true); err != nil {
		t.Fatal(err)
	}
	if f.repeat != "single" {
		t.Errorf("repeat %q", f.repeat)
	}
}

func TestAnotherProfileTakesOver(t *testing.T) {
	f, a, srv := setup(t)
	ctx := context.Background()
	id1, err := a.speakers.Sync(ctx, "u1", "One", Snapshot{Items: items(2), Playing: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := a.speakers.Sync(ctx, "u2", "Two", Snapshot{Items: items(2), Index: 1, Playing: true}, true)
	if err != nil || id2 == id1 {
		t.Fatalf("%v %s %s", err, id1, id2)
	}
	if code, _ := call(t, srv, "u1", "POST", "/api/session/pause", map[string]any{"session": id1}); code != 409 {
		t.Errorf("the old owner paused: %d", code)
	}
	st := a.speakers.Status(ctx, "u1")
	if st.Mine || st.Owner != "Two" || st.Session != id2 {
		t.Errorf("status for u1 %+v", st)
	}
	if code, _ := call(t, srv, "u2", "POST", "/api/session/stop", map[string]any{"session": id2}); code != 204 {
		t.Errorf("stop: %d", code)
	}
	if len(f.queue) != 0 || f.state != "stop" || f.outputs[1].Selected {
		t.Errorf("after stop %+v %s %+v", f.queue, f.state, f.outputs)
	}
	if st := a.speakers.Status(ctx, "u2"); st.Session != "" {
		t.Errorf("session left %+v", st)
	}
}

func TestPausedLoadSeeksAtPlay(t *testing.T) {
	f, a, _ := setup(t)
	ctx := context.Background()
	id, err := a.speakers.Sync(ctx, "u1", "P", Snapshot{Items: items(2), PositionSec: 12}, true)
	if err != nil {
		t.Fatal(err)
	}
	if f.state != "stop" {
		t.Fatalf("started: %s", f.state)
	}
	if st := a.speakers.Status(ctx, "u1"); st.PositionMs != 12000 {
		t.Errorf("waiting position %d", st.PositionMs)
	}
	if err := a.speakers.Play(ctx, "u1", id); err != nil {
		t.Fatal(err)
	}
	if f.state != "play" || f.progMs != 12000 {
		t.Errorf("play: %s %d", f.state, f.progMs)
	}
}

func TestNoOwnTone(t *testing.T) {
	fd := &Finder{Candidates: []string{"http://127.0.0.1:1"}}
	a := &addon{finder: fd, helper: addonkit.Helper{Name: "owntone", Image: "owntone/owntone:29.3", Port: 3689, Health: "/api/config"},
		speakers: &Speakers{ot: fd.OwnTone}}
	srv := httptest.NewServer(newHandler(a, readySet()))
	defer srv.Close()
	code, body := call(t, srv, "u1", "GET", "/api/state", nil)
	var st State
	json.Unmarshal(body, &st)
	if code != 200 || st.Ready || !strings.Contains(st.Compose, "network_mode: host") {
		t.Errorf("%d %+v", code, st)
	}
	if code, _ := call(t, srv, "u1", "POST", "/api/session", map[string]any{"snapshot": Snapshot{Items: items(1)}, "jump": true}); code != 503 {
		t.Errorf("session without OwnTone: %d", code)
	}
}

func TestManifestHelper(t *testing.T) {
	hs, err := addonkit.HelpersFromManifest(manifest)
	if err != nil || len(hs) != 1 || hs[0].Name != "owntone" {
		t.Fatalf("%v %+v", err, hs)
	}
}
