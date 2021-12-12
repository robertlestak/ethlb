package proxy

import (
	"encoding/json"
	"errors"
	"io/ioutil"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
)

var (
	Chains []Chain
)

type ChainEndpoint struct {
	Endpoint string `json:"endpoint"`
	Enabled  bool   `json:"enabled"`
}

type Chain struct {
	Name      string          `json:"name"`
	Endpoints []ChainEndpoint `json:"endpoints"`
	next      uint32
}

func UnmarshalJSON(data []byte) error {
	l := log.WithFields(log.Fields{"action": "UnmarshalJSON"})
	l.Info("unmarshalling config")
	var chains []Chain
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

func (c *Chain) EnabledEndpoints() []ChainEndpoint {
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

func (c *Chain) NextEndpoint() (string, error) {
	var es string
	if len(c.Endpoints) == 0 {
		return es, errors.New("no endpoints")
	}
	enabled := c.EnabledEndpoints()
	n := atomic.AddUint32(&c.next, 1)
	return enabled[(int(n)-1)%len(enabled)].Endpoint, nil
}

func GetEndpoint(chainName string) (string, error) {
	l := log.WithFields(log.Fields{
		"chain":  chainName,
		"action": "GetEndpoint",
	})
	l.Info("getting endpoint")
	for _, c := range Chains {
		if c.Name == chainName {
			return c.NextEndpoint()
		}
	}
	l.Error("failed to get endpoint")
	return "", errors.New("no such chain")
}
