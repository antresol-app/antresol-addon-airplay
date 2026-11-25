package main

// Where OwnTone is. It runs on the host's network (it finds the speakers
// over mDNS, which a compose network does not carry), so its compose name
// resolves only when the server shares no network with it: the addon tries
// the admin's address, else the compose name, then the host itself (the
// server on the host's network too) and the host as seen from a container
// (host.docker.internal, with `extra_hosts: host-gateway`).

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/antresol-app/antresol/addonkit"
)

// Candidates are the addresses tried when the admin gave none, in order.
var Candidates = []string{"http://owntone:3689", "http://127.0.0.1:3689", "http://host.docker.internal:3689"}

// Finder keeps the OwnTone address that answered last.
type Finder struct {
	// Override is the admin's address (ANTRESOL_AIRPLAY_OWNTONE_URL): the only one tried.
	Override string
	// Password is OwnTone's admin_password (ANTRESOL_AIRPLAY_OWNTONE_PASSWORD).
	Password   string
	Candidates []string
	HTTP       *http.Client
	Health     addonkit.HelperHealth

	mu     sync.Mutex
	found  string
	flight *probeFlight // the probe in progress
}

func (f *Finder) client() *http.Client {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// probeBudget bounds one probe of all the addresses.
const probeBudget = 20 * time.Second

// probeFlight is a probe in progress.
type probeFlight struct {
	done chan struct{}
	err  error
}

// Probe tries the addresses and keeps the first that answers as OwnTone.
// Probes asked while one runs wait for its answer (a picker polling an
// OwnTone that is not there must not start one probe per poll).
func (f *Finder) Probe(ctx context.Context) error {
	f.mu.Lock()
	fl := f.flight
	if fl == nil {
		fl = &probeFlight{done: make(chan struct{})}
		f.flight = fl
		// the flight outlives the caller that started it
		go func() {
			pctx, cancel := context.WithTimeout(context.Background(), probeBudget)
			defer cancel()
			fl.err = f.probe(pctx)
			f.mu.Lock()
			f.flight = nil
			f.mu.Unlock()
			close(fl.done)
		}()
	}
	f.mu.Unlock()
	select {
	case <-fl.done:
		return fl.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *Finder) probe(ctx context.Context) error {
	list := f.Candidates
	if f.Override != "" {
		list = []string{f.Override}
	}
	var first error
	for _, base := range list {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		o := &OwnTone{Base: strings.TrimRight(base, "/"), Password: f.Password, HTTP: f.client()}
		_, err := o.Config(pctx)
		cancel()
		if err == nil {
			f.mu.Lock()
			f.found = o.Base
			f.mu.Unlock()
			return nil
		}
		if first == nil {
			first = err
		}
	}
	f.mu.Lock()
	f.found = ""
	f.mu.Unlock()
	if first == nil {
		first = errors.New("no address")
	}
	return first
}

// OwnTone is the server found, or nil.
func (f *Finder) OwnTone() *OwnTone {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.found == "" {
		return nil
	}
	return &OwnTone{Base: f.found, Password: f.Password, HTTP: f.client()}
}

// State is the helper as helpers.status reports it.
func (f *Finder) State(ctx context.Context, h addonkit.Helper) addonkit.HelperState {
	st := addonkit.HelperState{Name: h.Name, Source: addonkit.HelperCompose}
	if f.Override != "" {
		st.Source = addonkit.HelperOverride
	} else if f.OwnTone() == nil {
		st.Source = addonkit.HelperNone
	}
	if st.Source != addonkit.HelperOverride {
		// never the admin's own address: it may carry a secret
		st.Address = h.Address()
	}
	return f.Health.State(ctx, st, f.Probe)
}
