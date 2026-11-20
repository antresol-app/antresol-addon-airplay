package main

// A client of OwnTone's JSON API (https://owntone.github.io/owntone-server/json-api/):
// the outputs it found on the network (AirPlay, Chromecast), its queue and
// its player. Only what the addon uses.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// OwnTone is one OwnTone server.
type OwnTone struct {
	// Base is its address, such as http://127.0.0.1:3689 (no trailing slash).
	Base string
	// Password is its admin_password, sent with HTTP basic auth when set
	// (OwnTone asks it of a client outside its trusted_networks).
	Password string
	HTTP     *http.Client
}

// APIError is an answer of OwnTone other than 2xx.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string { return fmt.Sprintf("owntone: status %d: %s", e.Status, e.Body) }

func (o *OwnTone) client() *http.Client {
	if o.HTTP != nil {
		return o.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// do sends method path?query with body (JSON when not nil) and decodes the
// answer into out (when not nil).
func (o *OwnTone) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	u := o.Base + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	if o.Password != "" {
		req.SetBasicAuth("admin", o.Password)
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data[:min(len(data), 300)]))}
	}
	if out == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Config is what GET /api/config answers (only what is used).
type Config struct {
	Version string `json:"version"`
}

// Config asks the server's config: the health check.
func (o *OwnTone) Config(ctx context.Context) (Config, error) {
	var c Config
	err := o.do(ctx, http.MethodGet, "/api/config", nil, nil, &c)
	if err == nil && c.Version == "" {
		err = errors.New("owntone: not an OwnTone answer")
	}
	return c, err
}

// Output is one of OwnTone's outputs.
type Output struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	Selected     bool   `json:"selected"`
	HasPassword  bool   `json:"has_password"`
	RequiresAuth bool   `json:"requires_auth"`
	NeedsAuthKey bool   `json:"needs_auth_key"`
	Volume       int    `json:"volume"`
}

// Outputs lists the outputs.
func (o *OwnTone) Outputs(ctx context.Context) ([]Output, error) {
	var r struct {
		Outputs []Output `json:"outputs"`
	}
	err := o.do(ctx, http.MethodGet, "/api/outputs", nil, nil, &r)
	return r.Outputs, err
}

// OutputChange is PUT /api/outputs/{id}: each field only when set.
type OutputChange struct {
	Selected *bool   `json:"selected,omitempty"`
	Volume   *int    `json:"volume,omitempty"`
	Pin      *string `json:"pin,omitempty"`
}

// ChangeOutput selects or deselects an output, sets its volume, or sends
// the PIN of a device's verification.
func (o *OwnTone) ChangeOutput(ctx context.Context, id string, c OutputChange) error {
	return o.do(ctx, http.MethodPut, "/api/outputs/"+url.PathEscape(id), nil, c, nil)
}

// SetOutputs selects exactly these outputs (none: every output off).
func (o *OwnTone) SetOutputs(ctx context.Context, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	return o.do(ctx, http.MethodPut, "/api/outputs/set", nil, map[string][]string{"outputs": ids}, nil)
}

// Player is GET /api/player.
type Player struct {
	State      string `json:"state"` // play, pause, stop
	Repeat     string `json:"repeat"`
	Shuffle    bool   `json:"shuffle"`
	Volume     int    `json:"volume"`
	ItemID     int    `json:"item_id"`
	LengthMs   int64  `json:"item_length_ms"`
	ProgressMs int64  `json:"item_progress_ms"`
}

func (o *OwnTone) Player(ctx context.Context) (Player, error) {
	var p Player
	err := o.do(ctx, http.MethodGet, "/api/player", nil, nil, &p)
	return p, err
}

func (o *OwnTone) put(ctx context.Context, path string, q url.Values) error {
	return o.do(ctx, http.MethodPut, path, q, nil, nil)
}

func (o *OwnTone) Play(ctx context.Context) error  { return o.put(ctx, "/api/player/play", nil) }
func (o *OwnTone) Pause(ctx context.Context) error { return o.put(ctx, "/api/player/pause", nil) }
func (o *OwnTone) Stop(ctx context.Context) error  { return o.put(ctx, "/api/player/stop", nil) }

// Seek moves to ms into the current item.
func (o *OwnTone) Seek(ctx context.Context, ms int64) error {
	return o.put(ctx, "/api/player/seek", url.Values{"position_ms": {strconv.FormatInt(ms, 10)}})
}

// SetVolume sets the master volume (0-100).
func (o *OwnTone) SetVolume(ctx context.Context, pct int) error {
	return o.put(ctx, "/api/player/volume", url.Values{"volume": {strconv.Itoa(pct)}})
}

// SetModes sets repeat (off, all, single) with shuffle and consume off:
// the order is the addon's.
func (o *OwnTone) SetModes(ctx context.Context, repeat string) error {
	if err := o.put(ctx, "/api/player/shuffle", url.Values{"state": {"false"}}); err != nil {
		return err
	}
	if err := o.put(ctx, "/api/player/consume", url.Values{"state": {"false"}}); err != nil {
		return err
	}
	return o.put(ctx, "/api/player/repeat", url.Values{"state": {repeat}})
}

// QueueItem is one item of OwnTone's queue.
type QueueItem struct {
	ID       int    `json:"id"`
	Position int    `json:"position"`
	URI      string `json:"uri"`
	LengthMs int64  `json:"length_ms"`
}

// Queue lists the queue.
func (o *OwnTone) Queue(ctx context.Context) ([]QueueItem, error) {
	var r struct {
		Items []QueueItem `json:"items"`
	}
	err := o.do(ctx, http.MethodGet, "/api/queue", nil, nil, &r)
	return r.Items, err
}

// ClearQueue empties the queue (and stops the player).
func (o *OwnTone) ClearQueue(ctx context.Context) error { return o.put(ctx, "/api/queue/clear", nil) }

// AddQueue adds the URLs (none may hold a comma: the API's separator) at
// position (-1: the end) and answers the new items in their order.
func (o *OwnTone) AddQueue(ctx context.Context, uris []string, position int) ([]QueueItem, error) {
	for _, u := range uris {
		if strings.Contains(u, ",") {
			return nil, fmt.Errorf("owntone: a URL with a comma")
		}
	}
	q := url.Values{"uris": {strings.Join(uris, ",")}}
	if position >= 0 {
		q.Set("position", strconv.Itoa(position))
	}
	var r struct {
		Items []QueueItem `json:"items"`
	}
	err := o.do(ctx, http.MethodPost, "/api/queue/items/add", q, nil, &r)
	if err == nil && len(r.Items) != len(uris) {
		err = fmt.Errorf("owntone: %d of %d items added", len(r.Items), len(uris))
	}
	return r.Items, err
}

// Tag is what the speakers show of an item.
type Tag struct{ Title, Artist, Album string }

// TagItem sets an item's title, artist and album (a URL item has none of its own).
func (o *OwnTone) TagItem(ctx context.Context, id int, t Tag) error {
	q := url.Values{}
	if t.Title != "" {
		q.Set("title", t.Title)
	}
	if t.Artist != "" {
		q.Set("artist", t.Artist)
		q.Set("album_artist", t.Artist)
	}
	if t.Album != "" {
		q.Set("album", t.Album)
	}
	if len(q) == 0 {
		return nil
	}
	return o.put(ctx, "/api/queue/items/"+strconv.Itoa(id), q)
}

// DeleteItem removes a queue item.
func (o *OwnTone) DeleteItem(ctx context.Context, id int) error {
	return o.do(ctx, http.MethodDelete, "/api/queue/items/"+strconv.Itoa(id), nil, nil, nil)
}
