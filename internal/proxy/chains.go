package proxy

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"sync/atomic"
	"time"

	"github.com/robertlestak/humun-chainmgr/internal/metrics"
	log "github.com/sirupsen/logrus"
)

var (
	Chains []*chain
)

type ChainEndpoint struct {
	Endpoint      string    `json:"endpoint"`
	Enabled       bool      `json:"enabled"`
	Failover      bool      `json:"failover"`
	CooldownUntil time.Time `json:"cooldownUntil"`
}

type chain struct {
	Name      string           `json:"name"`
	Endpoints []*ChainEndpoint `json:"endpoints"`
	next      uint32
}

type Chain interface {
	EnabledEndpoints() []*ChainEndpoint
	NextEndpoint() (string, error)
}

func UnmarshalJSON(data []byte) error {
	l := log.WithFields(log.Fields{"action": "UnmarshalJSON"})
	l.Info("unmarshalling config")
	var chains []*chain
	if err := json.Unmarshal(data, &chains); err != nil {
		return err
	}
	for _, c := range Chains {
		for _, ce := range c.Endpoints {
			for _, ch := range chains {
				for _, ce2 := range ch.Endpoints {
					if ce.Endpoint == ce2.Endpoint {
						ce2.Enabled = ce.Enabled
						ce2.CooldownUntil = ce.CooldownUntil
					}
				}
			}
		}
	}
	Chains = chains
	l.WithField("chains", len(Chains)).Info("unmarshalled config")
	for _, c := range Chains {
		l.WithFields(log.Fields{
			"chain":          c.Name,
			"endpointsCount": len(c.Endpoints),
			"endpoints":      c.Endpoints,
		}).Info("unmarshalled chain")
	}
	return nil
}

func LoadConfigFile(filename string) error {
	l := log.WithFields(log.Fields{"filename": filename, "action": "LoadConfigFile"})
	l.Info("loading config file")
	data, err := ioutil.ReadFile(filename)
	if err != nil {
		l.WithError(err).Error("failed to read config file")
		return err
	}
	return UnmarshalJSON(data)
}

func HotLoadConfigFile(filename string) error {
	l := log.WithFields(log.Fields{"filename": filename, "action": "HotLoadConfigFile"})
	l.Info("hot loading config file")
	if err := LoadConfigFile(filename); err != nil {
		l.WithError(err).Error("failed to hot load config file")
		return err
	}
	go func() {
		for {
			if err := LoadConfigFile(filename); err != nil {
				l.WithError(err).Fatal("failed to hot load config file")
			}
			l.Info("hot loaded config file")
			time.Sleep(time.Second * 60)
		}
	}()
	return nil
}

func (c *chain) EnabledEndpoints() []*ChainEndpoint {
	l := log.WithFields(log.Fields{
		"chain":  c.Name,
		"action": "EnabledEndpoints",
	})
	l.Info("getting enabled endpoints")
	var enabled []*ChainEndpoint
	var failover []*ChainEndpoint
	for _, e := range c.Endpoints {
		if !e.CooldownUntil.IsZero() {
			metrics.Cooldowns.WithLabelValues(e.Endpoint).Set(float64(e.CooldownUntil.Unix()))
		}
		if !e.Enabled && time.Now().After(e.CooldownUntil) {
			e.Enabled = true
			e.CooldownUntil = time.Time{}
		}
		if e.Enabled {
			enabled = append(enabled, e)
		}
		if e.Failover {
			failover = append(failover, e)
		}
	}
	if len(enabled) == 0 && len(failover) > 0 {
		l.WithField("failover", len(failover)).Info("using failover endpoints")
		return failover
	}
	l.WithField("enabled", len(enabled)).Info("enabled endpoints")
	return enabled
}

func CooldownEndpoint(chain string, e string) error {
	l := log.WithFields(log.Fields{
		"chain":    chain,
		"action":   "CooldownEndpoint",
		"endpoint": e,
	})
	l.Info("cooldown endpoint")
	cdur := time.Minute * 1
	var err error
	if os.Getenv("COOLDOWN_DURATION") != "" {
		cdur, err = time.ParseDuration(os.Getenv("COOLDOWN_DURATION"))
		if err != nil {
			l.WithError(err).Error("failed to parse cooldown duration")
			return err
		}
	}
	for _, c := range Chains {
		if c.Name == chain {
			for _, ce := range c.Endpoints {
				// only cool down if there are other enabled endpoints
				if ce.Endpoint == e && len(c.EnabledEndpoints()) > 1 {
					ce.Enabled = false
					ce.CooldownUntil = time.Now().Add(cdur)
					metrics.Cooldowns.WithLabelValues(e).Set(float64(ce.CooldownUntil.Unix()))
					l.Info("cooldown endpoint")
					return nil
				}
			}
		}
	}
	l.Error("failed to cooldown endpoint")
	return errors.New("no such endpoint")
}

func (c *chain) NextEndpoint() (string, error) {
	l := log.WithFields(log.Fields{
		"chain":  c.Name,
		"action": "NextEndpoint",
	})
	l.Info("getting next endpoint")
	var es string
	if len(c.Endpoints) == 0 {
		l.Error("no endpoints")
		return es, errors.New("no endpoints")
	}
	enabled := c.EnabledEndpoints()
	if len(enabled) == 0 {
		l.Error("no enabled endpoints")
		return es, errors.New("no enabled endpoints")
	}
	n := atomic.AddUint32(&c.next, 1)
	ne := enabled[(int(n)-1)%len(enabled)].Endpoint
	l.WithFields(log.Fields{
		"next":     n,
		"len":      len(enabled),
		"endpoint": ne,
	}).Info("next endpoint")
	return ne, nil
}

func GetEndpoint(chainName string) (string, error) {
	l := log.WithFields(log.Fields{
		"chain":  chainName,
		"action": "GetEndpoint",
	})
	l.Info("getting endpoint")
	for _, c := range Chains {
		if c.Name == chainName {
			ne, nerr := c.NextEndpoint()
			if nerr != nil {
				l.WithError(nerr).Error("failed to get next endpoint")
				return "", nerr
			}
			return ne, nil
		}
	}
	l.Error("failed to get endpoint")
	return "", errors.New("no such chain")
}
