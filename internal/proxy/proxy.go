package proxy

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/robertlestak/humun-chainmgr/internal/cache"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
)

type transport struct {
	http.RoundTripper
}

func (t *transport) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	l := log.WithFields(log.Fields{
		"method": req.Method,
		"url":    req.URL.String(),
	})
	l.Info("start")
	defer l.Info("end")
	l.Debug("round trip")
	vars := mux.Vars(req)
	chain := vars["chain"]
	var bd []byte
	if req.Body != nil {
		bd, err = httputil.DumpRequest(req, true)
		if err != nil {
			l.WithError(err).Error("failed to dump request")
			return nil, err
		}
	}
	rh := fmt.Sprintf("%x", md5.Sum(bd))
	cacheKey := chain + ":" + rh
	cd, cerr := cache.Get(cacheKey)
	l = l.WithField("cache", cacheKey)
	if cerr != nil {
		l.WithError(cerr).Error("get cache")
	}
	if cd != "" {
		l.Info("cache hit")
		decoded, derr := base64.StdEncoding.DecodeString(cd)
		if derr != nil {
			l.WithError(derr).Error("decode cache")
			return nil, derr
		}
		r := bufio.NewReader(bytes.NewReader(decoded))
		resp, err = http.ReadResponse(r, nil)
		if err != nil {
			l.WithError(err).Error("read response")
			return nil, err
		}
		return resp, nil
	}
	l.Info("cache miss")
	resp, err = t.RoundTripper.RoundTrip(req)
	if err != nil {
		l.WithError(err).Error("failed to round trip")
		return nil, err
	}
	l.Debug("read response")
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		l.WithError(err).Error("failed to read response")
		return nil, err
	}
	l.Debug("get response body")
	err = resp.Body.Close()
	if err != nil {
		l.WithError(err).Error("failed to close response body")
		return nil, err
	}
	l.Debug("set response body")
	body := ioutil.NopCloser(bytes.NewReader(b))
	resp.Body = body
	if resp.StatusCode == 200 {
		l.Debug("set cache")
		cacheTTL := time.Minute * 10
		rd, err := httputil.DumpResponse(resp, true)
		if err != nil {
			l.WithError(err).Error("failed to dump response")
			return nil, err
		}
		encoded := base64.StdEncoding.EncodeToString(rd)
		cerr = cache.Set(cacheKey, encoded, cacheTTL)
		if cerr != nil {
			l.WithError(cerr).Error("failed to set cache")
		}
	}
	return resp, nil
}

func Handler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	chain := vars["chain"]
	l := log.WithFields(log.Fields{
		"remote": r.RemoteAddr,
		"url":    r.URL.String(),
		"chain":  chain,
		"action": "proxy.Handler",
	})
	l.Info("start")
	defer l.Info("end")
	l.Debug("get endpoint")
	endpoint, err := GetEndpoint(chain)
	if err != nil {
		l.WithError(err).Error("failed to get endpoint")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	l.Debug("create director")
	d := func(req *http.Request) {
		u, err := url.Parse(endpoint)
		if err != nil {
			l.WithError(err).Error("failed to parse endpoint")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		req.Host = u.Host
		req.URL.Path = u.Path
		l.Printf("proxying to %s", req.URL.String())
	}
	l.Debug("create error handler")
	e := func(w http.ResponseWriter, r *http.Request, e error) {
		l.WithError(e).Error("failed to proxy")
		http.Error(w, e.Error(), http.StatusBadGateway)
	}
	l.Debug("create transport")
	defaultTransport := http.DefaultTransport.(*http.Transport)
	customTransport := &http.Transport{
		Proxy:                 defaultTransport.Proxy,
		DialContext:           defaultTransport.DialContext,
		MaxIdleConns:          defaultTransport.MaxIdleConns,
		IdleConnTimeout:       defaultTransport.IdleConnTimeout,
		ExpectContinueTimeout: defaultTransport.ExpectContinueTimeout,
		TLSHandshakeTimeout:   defaultTransport.TLSHandshakeTimeout,
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
	}
	l.Debug("create proxy")
	p := &httputil.ReverseProxy{
		Director:     d,
		ErrorHandler: e,
		Transport:    &transport{customTransport},
	}
	l.Debug("proxy request")
	p.ServeHTTP(w, r)
}
