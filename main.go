// The «Колонки дома» addon (contract 1, docs/addons/contract.md in the
// core): «Воспроизвести на…» of the music player. OwnTone, a helper service
// on the host's network, finds the AirPlay and Chromecast speakers of the
// home network; the browser hands the addon its queue (stream URLs with
// the profile's media key) and the addon has OwnTone play it on the
// speakers the profile picked.
//
// Variables (ANTRESOL_AIRPLAY_*):
//   OWNTONE_URL       OwnTone's address (default: the compose name, then
//                     the host: see find.go)
//   OWNTONE_PASSWORD  OwnTone's admin_password, when it asks one
//   STREAM_URL        the server's address as OwnTone reaches it (default:
//                     the address the browser uses)
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/antresol-app/antresol/addonkit"
)

func main() {
	env, err := addonkit.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(env.DataDir, 0o700); err != nil {
		log.Fatalf("data dir: %v", err)
	}
	helpers, err := addonkit.HelpersFromManifest(manifest)
	if err != nil || len(helpers) != 1 {
		log.Fatalf("manifest helpers: %v", err)
	}
	a, err := fromEnv(env, helpers[0])
	if err != nil {
		log.Fatal(err)
	}
	os.Remove(env.Socket) // a stale socket of an earlier run
	ln, err := net.Listen("unix", env.Socket)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	ready := &addonkit.Ready{}
	srv := &http.Server{Handler: newHandler(a, ready), ReadHeaderTimeout: 10 * time.Second}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shut)
	}()
	watch := &addonkit.HelperWatch{Helper: helpers[0], Probe: a.finder.Probe, Health: &a.finder.Health,
		OnChange: func(found bool) { log.Printf("airplay: OwnTone found: %v", found) }}
	go func() {
		watch.Check(ctx)
		watch.Run(ctx)
	}()
	go a.speakers.Run(ctx)
	ready.Set()
	log.Printf("%s: serving on %s", env.ID, env.Socket)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

// fromEnv builds the addon from its variables.
func fromEnv(env addonkit.Env, h addonkit.Helper) (*addon, error) {
	f := &Finder{Override: env.Var("OWNTONE_URL"), Password: env.Var("OWNTONE_PASSWORD"), Candidates: Candidates}
	if f.Override != "" {
		if u, err := url.Parse(f.Override); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, errors.New("ANTRESOL_AIRPLAY_OWNTONE_URL: not an http(s) address")
		}
	}
	a := &addon{finder: f, helper: h, speakers: &Speakers{ot: f.OwnTone}}
	if s := env.Var("STREAM_URL"); s != "" {
		u, err := url.Parse(s)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
			return nil, errors.New("ANTRESOL_AIRPLAY_STREAM_URL: not an http(s) address")
		}
		a.streamBase = u
	}
	return a, nil
}
