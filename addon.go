package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/antresol-app/antresol/addonkit"
)

//go:embed antresol-addon.json
var manifest []byte

// OutputView is an output as the picker shows it.
type OutputView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Kind: airplay, chromecast or other.
	Kind     string `json:"kind"`
	Type     string `json:"type"`
	Selected bool   `json:"selected"`
	Volume   int    `json:"volume"`
	// NeedsPin: the device asks for a PIN it shows (pairing, a HomePod or an Apple TV).
	NeedsPin bool `json:"needsPin,omitempty"`
	// Locked: a password-protected device (its password goes in OwnTone's own config).
	Locked bool `json:"locked,omitempty"`
}

// hidden: OwnTone's own sound card or sound server, nothing of the home network.
var hidden = []string{"alsa", "pulseaudio", "pipewire"}

func outputView(o Output) (OutputView, bool) {
	t := strings.ToLower(o.Type)
	if slices.Contains(hidden, t) {
		return OutputView{}, false
	}
	kind := "other"
	switch {
	case strings.HasPrefix(t, "airplay"):
		kind = "airplay"
	case strings.HasPrefix(t, "chromecast"):
		kind = "chromecast"
	}
	return OutputView{ID: o.ID, Name: o.Name, Kind: kind, Type: o.Type, Selected: o.Selected, Volume: o.Volume,
		NeedsPin: o.NeedsAuthKey || o.RequiresAuth, Locked: o.HasPassword}, true
}

// State is GET /api/state: whether OwnTone is there, its outputs, the session.
type State struct {
	Ready   bool         `json:"ready"`
	Outputs []OutputView `json:"outputs"`
	Status  Status       `json:"status"`
	// Compose is the block to add while OwnTone is not found (admins only).
	Compose string `json:"compose,omitempty"`
}

type addon struct {
	finder   *Finder
	speakers *Speakers
	helper   addonkit.Helper
	// streamBase is ANTRESOL_AIRPLAY_STREAM_URL: where OwnTone reaches the server, when not at the browser's address
	streamBase *url.URL
}

var outputID = regexp.MustCompile(`^[0-9A-Za-z_-]{1,40}$`)

const callTimeout = 40 * time.Second

func newHandler(a *addon, ready *addonkit.Ready) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", ready.Health)

	mux.HandleFunc("GET /api/state", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		defer cancel()
		st := State{Outputs: []OutputView{}}
		o := a.finder.OwnTone()
		if o == nil {
			// asked now, not at the next 30 s check: the admin has just started it
			if a.finder.Probe(ctx) == nil {
				o = a.finder.OwnTone()
			}
		}
		if o != nil {
			outs, err := o.Outputs(ctx)
			a.finder.Health.Record(err)
			if err == nil {
				st.Ready = true
				for _, x := range outs {
					if v, ok := outputView(x); ok {
						st.Outputs = append(st.Outputs, v)
					}
				}
			} else {
				log.Printf("airplay: outputs: %v", err)
			}
		}
		if !st.Ready && u.IsAdmin {
			st.Compose = composeBlock(a.helper)
		}
		st.Status = a.speakers.Status(ctx, u.UID)
		addonkit.WriteJSON(w, http.StatusOK, st)
	}))

	mux.HandleFunc("GET /api/session", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		defer cancel()
		addonkit.WriteJSON(w, http.StatusOK, a.speakers.Status(ctx, u.UID))
	}))

	mux.HandleFunc("PUT /api/outputs/{id}", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		id := r.PathValue("id")
		var body struct {
			Selected *bool `json:"selected"`
			Volume   *int  `json:"volume"`
		}
		if !outputID.MatchString(id) || !decode(w, r, &body) {
			return
		}
		if body.Volume != nil && (*body.Volume < 0 || *body.Volume > 100) {
			addonkit.WriteError(w, http.StatusBadRequest, "Неверная громкость")
			return
		}
		a.ownTone(w, r, func(ctx context.Context, o *OwnTone) error {
			return o.ChangeOutput(ctx, id, OutputChange{Selected: body.Selected, Volume: body.Volume})
		})
	}))

	// exactly these outputs play (none: all off): picking a speaker while the browser plays
	mux.HandleFunc("PUT /api/outputs", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		var body struct {
			IDs []string `json:"ids"`
		}
		if !decode(w, r, &body) {
			return
		}
		if len(body.IDs) > 64 || slices.ContainsFunc(body.IDs, func(id string) bool { return !outputID.MatchString(id) }) {
			addonkit.WriteError(w, http.StatusBadRequest, "Не удалось разобрать запрос")
			return
		}
		a.ownTone(w, r, func(ctx context.Context, o *OwnTone) error { return o.SetOutputs(ctx, body.IDs) })
	}))

	mux.HandleFunc("POST /api/outputs/{id}/pin", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		id := r.PathValue("id")
		var body struct {
			Pin string `json:"pin"`
		}
		if !outputID.MatchString(id) || !decode(w, r, &body) {
			return
		}
		if !regexp.MustCompile(`^[0-9]{4}$`).MatchString(body.Pin) {
			addonkit.WriteError(w, http.StatusBadRequest, "Нужен код из четырёх цифр")
			return
		}
		a.ownTone(w, r, func(ctx context.Context, o *OwnTone) error {
			sel := true
			return o.ChangeOutput(ctx, id, OutputChange{Pin: &body.Pin, Selected: &sel})
		})
	}))

	mux.HandleFunc("POST /api/session", a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
		var body struct {
			Snapshot Snapshot `json:"snapshot"`
			Jump     bool     `json:"jump"`
		}
		if !decodeLimit(w, r, &body, 8<<20) {
			return
		}
		if err := body.Snapshot.Check(a.streamBase); err != nil {
			addonkit.WriteError(w, http.StatusBadRequest, "Очередь не подходит для колонок")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
		defer cancel()
		id, err := a.speakers.Sync(ctx, u.UID, u.Name, body.Snapshot, body.Jump)
		if err != nil {
			a.fail(w, err)
			return
		}
		addonkit.WriteJSON(w, http.StatusOK, map[string]string{"session": id})
	}))

	type control struct {
		Session string  `json:"session"`
		Ms      *int64  `json:"ms"`
		Volume  *int    `json:"volume"`
	}
	act := func(name string, fn func(ctx context.Context, uid string, c control) error) {
		mux.HandleFunc("POST /api/session/"+name, a.withUser(func(w http.ResponseWriter, r *http.Request, u addonkit.User) {
			var c control
			if !decode(w, r, &c) {
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
			defer cancel()
			if err := fn(ctx, u.UID, c); err != nil {
				a.fail(w, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
	}
	act("play", func(ctx context.Context, uid string, c control) error { return a.speakers.Play(ctx, uid, c.Session) })
	act("pause", func(ctx context.Context, uid string, c control) error { return a.speakers.Pause(ctx, uid, c.Session) })
	act("stop", func(ctx context.Context, uid string, c control) error { return a.speakers.Stop(ctx, uid, c.Session) })
	act("seek", func(ctx context.Context, uid string, c control) error {
		if c.Ms == nil || *c.Ms < 0 || *c.Ms > 1e10 {
			return errBadRequest
		}
		return a.speakers.Seek(ctx, uid, c.Session, *c.Ms)
	})
	act("volume", func(ctx context.Context, uid string, c control) error {
		if c.Volume == nil || *c.Volume < 0 || *c.Volume > 100 {
			return errBadRequest
		}
		return a.speakers.Volume(ctx, uid, c.Session, *c.Volume)
	})

	hooks := &addonkit.HookMux{}
	hooks.Handle("helpers.status", func(ctx context.Context, _ *addonkit.User, _ json.RawMessage) (any, error) {
		return addonkit.HelpersStatus{Helpers: []addonkit.HelperState{a.finder.State(ctx, a.helper)}}, nil
	})
	mux.Handle("POST /hooks/", hooks)
	return mux
}

var errBadRequest = errors.New("bad request")

// withUser: every route is a profile's (the server sends adults only: access "adults").
func (a *addon) withUser(h func(http.ResponseWriter, *http.Request, addonkit.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := addonkit.UserFrom(r)
		if !ok || u.UID == "" {
			addonkit.WriteError(w, http.StatusUnauthorized, "Нужно войти")
			return
		}
		if u.IsKid {
			addonkit.WriteError(w, http.StatusForbidden, "Недоступно в детском профиле")
			return
		}
		h(w, r, *u)
	}
}

func (a *addon) ownTone(w http.ResponseWriter, r *http.Request, fn func(ctx context.Context, o *OwnTone) error) {
	o := a.finder.OwnTone()
	if o == nil {
		a.fail(w, ErrNoOwnTone)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), callTimeout)
	defer cancel()
	err := fn(ctx, o)
	a.finder.Health.Record(err)
	if err != nil {
		a.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail answers an error in the words the picker shows.
func (a *addon) fail(w http.ResponseWriter, err error) {
	var api *APIError
	switch {
	case errors.Is(err, errBadRequest):
		addonkit.WriteError(w, http.StatusBadRequest, "Не удалось разобрать запрос")
	case errors.Is(err, ErrNoOwnTone):
		addonkit.WriteError(w, http.StatusServiceUnavailable, "Служба колонок (OwnTone) не найдена")
	case errors.Is(err, ErrNoSession):
		addonkit.WriteError(w, http.StatusConflict, "Колонки уже играют другое")
	case errors.As(err, &api) && (api.Status == http.StatusUnauthorized || api.Status == http.StatusForbidden):
		log.Printf("airplay: %v", err)
		addonkit.WriteError(w, http.StatusBadGateway, "OwnTone не пускает расширение: проверьте пароль")
	default:
		log.Printf("airplay: %v", err)
		addonkit.WriteError(w, http.StatusBadGateway, "Колонки не ответили")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool { return decodeLimit(w, r, v, 64<<10) }

func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		addonkit.WriteError(w, http.StatusRequestEntityTooLarge, "Слишком большой запрос")
		return false
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		data = []byte("{}")
	}
	if err := json.Unmarshal(data, v); err != nil {
		addonkit.WriteError(w, http.StatusBadRequest, "Не удалось разобрать запрос")
		return false
	}
	return true
}

// composeBlock is the compose service to add for OwnTone: on the host's
// network (mDNS), with its config and database kept.
func composeBlock(h addonkit.Helper) string {
	return "  " + h.Name + ":\n" +
		"    image: " + h.Image + "\n" +
		"    restart: unless-stopped\n" +
		"    # OwnTone finds the speakers over mDNS: the host's network, not the project's\n" +
		"    network_mode: host\n" +
		"    volumes:\n" +
		"      - ./owntone/etc:/etc/owntone\n" +
		"      - ./owntone/cache:/var/cache/owntone\n"
}
