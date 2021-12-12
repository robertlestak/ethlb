package proxy

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

var (
	Chains []*chain
)

type ChainEndpoint struct {
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

type chain struct {
	Name      string          `json:"name"`
	Endpoints []ChainEndpoint `json:"endpoints"`
	next      uint32
}

type Chain interface {
	EnabledEndpoints() []ChainEndpoint
	NextEndpoint() (string, error)
}

func UnmarshalJSON(data []byte) error {
	l := log.WithFields(log.Fields{"action": "UnmarshalJSON"})
	l.Info("unmarshalling config")
	var chains []*chain
	if err := json.Unmarshal(data, &chains); err != nil {
		return err
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

func (c *chain) EnabledEndpoints() []ChainEndpoint {
	l := log.WithFields(log.Fields{
		"chain":  c.Name,
		"action": "EnabledEndpoints",
	})
	l.Info("getting enabled endpoints")
	var enabled []ChainEndpoint
	for _, e := range c.Endpoints {
		if e.Enabled {
			enabled = append(enabled, e)
		}
	}
	l.WithField("enabled", len(enabled)).Info("enabled endpoints")
	return enabled
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
