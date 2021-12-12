package main

import (
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/robertlestak/humun-chainmgr/internal/cache"
	"github.com/robertlestak/humun-chainmgr/internal/proxy"
	log "github.com/sirupsen/logrus"
)

func init() {
	ll := log.InfoLevel
	if os.Getenv("LOG_LEVEL") != "" {
		var err error
		ll, err = log.ParseLevel(os.Getenv("LOG_LEVEL"))
		if err != nil {
			log.Fatal(err)
		}
	}
	log.SetLevel(ll)
	lerr := proxy.LoadConfigFile(os.Getenv("CONFIG_FILE"))
	if lerr != nil {
		log.WithError(lerr).Fatal("failed to load config file")
	}
	if ierr := cache.Init(); ierr != nil {
		log.WithError(ierr).Fatal("failed to init cache")
	}
}

func main() {
	l := log.WithFields(log.Fields{
		"action": "main",
	})
	l.Info("start")
	r := mux.NewRouter()
	r.HandleFunc("/{chain}", proxy.Handler)
	port := "8080"
	if os.Getenv("PORT") != "" {
		port = os.Getenv("PORT")
	}
	l.Info("listening on port " + port)
	log.Fatal(http.ListenAndServe(":"+port, r))
}
