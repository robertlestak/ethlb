package proxy

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/md5"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/robertlestak/humun-chainmgr/internal/cache"
	"github.com/robertlestak/humun-chainmgr/internal/metrics"

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

type JSONRPCContainer struct {
	Single *JSONRPCResponse
	Batch  []JSONRPCResponse
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
	l.Debug("start")
	defer l.Debug("end")
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
	l.Debug("start")
	defer l.Debug("end")
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

func debugReqResp(req *http.Request, resp *http.Response) error {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "debugReqResp",
	})
	l.Debug("start")
	defer l.Debug("end")
	var err error
	var reqDump []byte
	var respDump []byte
	reqDump, err = httputil.DumpRequest(req, true)
	if err != nil {
		l.WithError(err).Error("dump request")
		return err
	}
	l.WithField("request", string(reqDump)).Debug("request")
	respDump, err = httputil.DumpResponse(resp, true)
	if err != nil {
		l.WithError(err).Error("dump response")
		return err
	}
	l.WithField("response", string(respDump)).Debug("response")
	return nil
}

func (c *JSONRPCContainer) unmarshalSingle(b []byte) error {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "unmarshalSingle",
	})
	l.Debug("start")
	defer l.Debug("end")
	var err error
	err = json.Unmarshal(b, &c.Single)
	if err != nil {
		l.WithField("body", string(b)).WithError(err).Error("failed to unmarshal jsonrpc response")
	}
	return err
}

func (c *JSONRPCContainer) unmarshalMany(b []byte) error {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "unmarshalMany",
	})
	l.Debug("start")
	defer l.Debug("end")
	err := json.Unmarshal(b, &c.Batch)
	if err != nil {
		l.WithField("body", string(b)).WithError(err).Error("failed to unmarshal jsonrpc response")
	}
	return err
}

func (c *JSONRPCContainer) Unmarshal(b []byte) error {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "Unmarshal",
	})
	l.Debug("start")
	defer l.Debug("end")
	switch b[0] {
	case '{':
		return c.unmarshalSingle(b)
	case '[':
		return c.unmarshalMany(b)
	}
	err := c.unmarshalMany(b)
	if err != nil {
		return c.unmarshalSingle(b)
	}
	return nil
}

func (t *transport) reqRoundTripper(req *http.Request, cacheKey string) (resp *http.Response, err error) {
	l := log.WithFields(log.Fields{
		"package": "proxy",
		"method":  "reqRoundTripper",
	})
	l.Debug("start")
	defer l.Debug("end")
	var cerr error
	resp, err = t.RoundTripper.RoundTrip(req)
	if err != nil {
		l.WithError(err).Error("failed to round trip")
		return nil, err
	}
	l.Debug("read response")
	if log.GetLevel() >= log.DebugLevel {
		if derr := debugReqResp(req, resp); derr != nil {
			l.WithError(derr).Error("debug request response")
		}
	}
	var reader io.ReadCloser
	var origResponseData []byte
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		l.WithError(err).Error("failed to read response")
		return nil, err
	}
	origResponseData = b
	switch resp.Header.Get("Content-Encoding") {
	case "gzip":
		reader, err = gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			l.WithError(err).Error("failed to create gzip reader")
			return nil, err
		}
		defer reader.Close()
	default:
		reader = ioutil.NopCloser(bytes.NewReader(b))
	}
	l.Debug("get response body")
	resp.Body = ioutil.NopCloser(bytes.NewReader(origResponseData))
	pd, perr := ioutil.ReadAll(reader)
	if perr != nil {
		l.WithError(perr).Error("failed to read response body")
		return nil, perr
	}
	l.Debug("set response body")
	rpcres := JSONRPCContainer{}
	l.Debugf("parse response body: %s", string(pd))
	if len(pd) > 0 {
		err = rpcres.Unmarshal(pd)
	}
	cacheable := false
	if resp.StatusCode == http.StatusOK &&
		os.Getenv("CACHE_DISABLED") != "true" &&
		((rpcres.Single != nil && rpcres.Single.Result != nil) ||
			(rpcres.Batch != nil && len(rpcres.Batch) > 0)) {
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
	l.Debug("start")
	defer l.Debug("end")
	if req.Body != nil {
		defer req.Body.Close()
	}
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
	// if server supports cache, and client does not have humun-cache=false header, cache
	if os.Getenv("CACHE_DISABLED") != "true" && req.Header.Get("humun-cache") != "false" {
		cd, cerr := cache.Get(cacheKey)
		l = l.WithField("cache", cacheKey)
		if cerr != nil {
			l.WithError(cerr).Error("get cache")
		}
		if cd != "" {
			l.Debug("cache hit")
			resp, err = respFromCache(cd)
			if err != nil {
				l.WithError(err).Error("failed to read response from cache")
				return nil, err
			}
			resp.Header.Set("x-humun-cache", "hit")
			metrics.CacheHit.WithLabelValues(chain, strconv.Itoa(resp.StatusCode), req.Method).Inc()
			return resp, nil
		}
	}
	l.Debug("cache miss")
	var retries int
	for retries < maxRetries {
		l = l.WithField("retry", retries)
		l.Debugf("round trip %+v", req)
		l.Debugf("body dump %+s", rbd)
		req.Body = ioutil.NopCloser(bytes.NewReader(rbd))
		resp, err = t.reqRoundTripper(req, cacheKey)
		if err != nil {
			l.WithError(err).Error("failed to round trip")
			retries++
			time.Sleep(retryDelay)
			continue
		}
		defer resp.Body.Close()
		l.Debugf("check response code %d", resp.StatusCode)
		if intInSlice(resp.StatusCode, retryableCodes) {
			l.WithField("status", resp.StatusCode).Debug("retryable status code")
			retries++
			time.Sleep(retryDelay)
			continue
		} else {
			l.WithField("status", resp.StatusCode).Debug("non-retryable status code")
			break
		}
	}
	if retries >= maxRetries {
		l.Error("max retries reached")
		if cerr := CooldownEndpoint(chain, req.URL.String()); cerr != nil {
			l.WithError(cerr).Error("failed to cooldown endpoint")
		}
		var retErr error
		if err != nil {
			retErr = err
		} else if resp.Body != nil {
			return resp, retErr
		} else {
			retErr = errors.New("connection error. please try again")
		}
		return nil, retErr
	}
	resp.Header.Set("x-humun-cache", "miss")
	l.Debug("return response")
	l.Debugf("response %+v", resp.StatusCode)
	metrics.HTTPRequests.WithLabelValues(req.URL.String(), strconv.Itoa(resp.StatusCode), req.Method).Inc()
	metrics.CacheMiss.WithLabelValues(chain, strconv.Itoa(resp.StatusCode), req.Method).Inc()
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
	l.Debug("start")
	defer l.Debug("end")
	l.Debug("get endpoint")
	var readOnly bool
	if strings.HasSuffix(r.URL.Path, "/read") {
		readOnly = true
		l.Debug("read only endpoint")
	}
	endpoint, err := GetEndpoint(chain, readOnly)
	if err != nil {
		l.WithError(err).Error("failed to get endpoint")
		w.WriteHeader(http.StatusInternalServerError)
		metrics.HTTPRequests.WithLabelValues(r.URL.String(), strconv.Itoa(http.StatusInternalServerError), r.Method).Inc()
		return
	}
	l.Debug("create director")
	d := func(req *http.Request) {
		u, err := url.Parse(endpoint)
		if err != nil {
			l.WithError(err).Error("failed to parse endpoint")
			w.WriteHeader(http.StatusInternalServerError)
			metrics.HTTPRequests.WithLabelValues(req.URL.String(), strconv.Itoa(http.StatusInternalServerError), r.Method).Inc()
			return
		}
		req.URL.Scheme = u.Scheme
		req.URL.Host = u.Host
		req.Host = u.Host
		req.URL.Path = u.Path
		l.Debugf("proxying to %s", req.URL.String())
	}
	l.Debug("create error handler")
	e := func(w http.ResponseWriter, r *http.Request, e error) {
		l.WithError(e).Error("failed to proxy")
		http.Error(w, e.Error(), http.StatusBadGateway)
		metrics.HTTPRequests.WithLabelValues(r.URL.String(), strconv.Itoa(http.StatusBadGateway), r.Method).Inc()
	}
	l.Debug("create transport")
	defaultTransport := http.DefaultTransport.(*http.Transport)
	customTransport := &http.Transport{
		Proxy:                 defaultTransport.Proxy,
		DialContext:           defaultTransport.DialContext,
		MaxIdleConns:          10000,
		MaxIdleConnsPerHost:   10000,
		DisableKeepAlives:     true,
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
