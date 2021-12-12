package proxy

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robertlestak/humun-chainmgr/internal/cache"

	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
)

var (
	maxRetries     = 3
	retryDelay     = time.Second * 5
	retryableCodes = []int{
		429,
		502,
		503,
		504,
	}
	cacheTTL = time.Minute * 10
)

type transport struct {
	http.RoundTripper
}

type JSONRPCResponse struct {
	Jsonrpc string      `json:"jsonrpc"`
	ID      int         `json:"id"`
	Result  interface{} `json:"result"`
}

func intInSlice(a int, list []int) bool {
	for _, b := range list {
		if b == a {
			return true
		}
	}
	return false
}

func ConfigRetryHandler() error {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "ConfigRetryHandler",
	})
	l.Info("start")
	defer l.Info("end")
	var err error
	if os.Getenv("MAX_RETRIES") != "" {
		maxRetries, err = strconv.Atoi(os.Getenv("MAX_RETRIES"))
		if err != nil {
			l.WithError(err).Error("failed to parse MAX_RETRIES")
			return err
		}
	}
	if os.Getenv("RETRY_DELAY") != "" {
		retryDelay, err = time.ParseDuration(os.Getenv("RETRY_DELAY"))
		if err != nil {
			l.WithError(err).Error("failed to parse RETRY_DELAY")
			return err
		}
	}
	if os.Getenv("RETRYABLE_CODES") != "" {
		retryableCodes = []int{}
		for _, code := range strings.Split(os.Getenv("RETRYABLE_CODES"), ",") {
			c, err := strconv.Atoi(code)
			if err != nil {
				l.WithError(err).Error("failed to parse RETRYABLE_CODES")
				return err
			}
			retryableCodes = append(retryableCodes, c)
		}
	}
	if os.Getenv("CACHE_TTL") != "" {
		cacheTTL, err = time.ParseDuration(os.Getenv("CACHE_TTL"))
		if err != nil {
			l.WithError(err).Error("failed to parse CACHE_TTL")
			return err
		}
	}
	return nil
}

func respFromCache(cd string) (resp *http.Response, err error) {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "respFromCache",
	})
	l.Info("start")
	defer l.Info("end")
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

func (t *transport) reqRoundTripper(req *http.Request, cacheKey string) (resp *http.Response, err error) {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "reqRoundTripper",
	})
	l.Info("start")
	defer l.Info("end")
	var cerr error
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
	defer resp.Body.Close()
	l.Debug("set response body")
	body := ioutil.NopCloser(bytes.NewReader(b))
	resp.Body = body
	rpcres := JSONRPCResponse{}
	err = json.Unmarshal(b, &rpcres)
	if err != nil {
		l.WithError(err).Error("failed to unmarshal response")
	}
	cacheable := false
	if resp.StatusCode == http.StatusOK && os.Getenv("CACHE_DISABLED") != "true" && rpcres.Result != nil {
		cacheable = true
	}
	l.Debug("cacheable: ", cacheable)
	if cacheable {
		l.Debug("set cache")
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
	var rbd []byte
	if req.Body != nil {
		// drop host from cache key
		req.Header.Set("Host", "")
		bd, err = httputil.DumpRequest(req, true)
		if err != nil {
			l.WithError(err).Error("failed to dump request")
			return nil, err
		}
		rbd, err = ioutil.ReadAll(req.Body)
		if err != nil {
			l.WithError(err).Error("failed to read request body")
			return nil, err
		}
	}
	rh := fmt.Sprintf("%x", md5.Sum(bd))
	cacheKey := chain + ":" + rh
	if os.Getenv("CACHE_DISABLED") != "true" {
		cd, cerr := cache.Get(cacheKey)
		l = l.WithField("cache", cacheKey)
		if cerr != nil {
			l.WithError(cerr).Error("get cache")
		}
		if cd != "" {
			l.Info("cache hit")
			resp, err = respFromCache(cd)
			if err != nil {
				l.WithError(err).Error("failed to read response from cache")
				return nil, err
			}
			return resp, nil
		}
	}
	l.Info("cache miss")
	var retries int
	for retries < maxRetries {
		l = l.WithField("retry", retries)
		l.Debugf("round trip %+v", req)
		l.Debugf("body dump %+s", rbd)
		req.Body = ioutil.NopCloser(bytes.NewReader(rbd))
		resp, err = t.reqRoundTripper(req, cacheKey)
		if err != nil {
			l.WithError(err).Error("failed to round trip")
			return nil, err
		}
		l.Debugf("check response code %d", resp.StatusCode)
		if intInSlice(resp.StatusCode, retryableCodes) {
			l.WithField("status", resp.StatusCode).Debug("retryable status code")
			retries++
			time.Sleep(retryDelay)
		} else {
			l.WithField("status", resp.StatusCode).Debug("non-retryable status code")
			break
		}
	}
	l.Debug("return response")
	l.Debugf("response %+v", resp.StatusCode)
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
