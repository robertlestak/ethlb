package proxy

import (
	"context"
	"encoding/json"
	"errors"
	"io/ioutil"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/robertlestak/humun-chainmgr/internal/metrics"
	log "github.com/sirupsen/logrus"
)

var (
	Chains []*chain
)

type ChainEndpoint struct {
	Endpoint      string            `json:"endpoint"`
	Enabled       bool              `json:"enabled"`
	Failover      bool              `json:"failover"`
	ReadOnly      bool              `json:"readOnly"`
	CooldownUntil time.Time         `json:"cooldownUntil"`
	BlockHead     uint64            `json:"blockHead"`
	Client        *ethclient.Client `json:"-"`
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

func CreateChainClients() error {
	l := log.WithFields(log.Fields{"func": "CreateChainClients"})
	l.Info("start")
	defer l.Info("end")
	// loop all chains
	for _, c := range Chains {
		// loop each chain endpoints
		for _, ce := range c.Endpoints {
			// if endpoint is enabled but client is nil, connect to it
			if ce.Enabled && ce.Client == nil {
				l.WithFields(log.Fields{
					"endpoint": ce.Endpoint,
				}).Info("creating ethclient")
				client, err := ethclient.Dial(ce.Endpoint)
				if err != nil {
					l.WithError(err).Error("failed to create ethclient")
					return err
				}
				// set client
				ce.Client = client
			}
		}
	}
	return nil
}

func UnmarshalJSON(data []byte) error {
	l := log.WithFields(log.Fields{"action": "UnmarshalJSON"})
	l.Info("unmarshalling config")
	var chains []*chain
	if err := json.Unmarshal(data, &chains); err != nil {
		return err
	}
	// loop all current chains
	for _, c := range Chains {
		// loop each chain endpoints
		for _, ce := range c.Endpoints {
			// loop over all new chains
			for _, ch := range chains {
				// loop over each new chain endpoints
				for _, ce2 := range ch.Endpoints {
					// if update is found, update
					if ce.Endpoint == ce2.Endpoint {
						ce2.Enabled = ce.Enabled
						ce2.CooldownUntil = ce.CooldownUntil
						ce2.Client = ce.Client
					}
				}
			}
		}
	}
	Chains = chains
	if cerr := CreateChainClients(); cerr != nil {
		l.WithError(cerr).Error("failed to create chain clients")
		return cerr
	}
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
	if err := UpdateChainBlockHeads(context.Background()); err != nil {
		l.WithError(err).Error("failed to update chain block heads")
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
		// if endpoint is disabled due to cooldown, but the cooldown is over, re-enable the endpoint
		if !e.Enabled && time.Now().After(e.CooldownUntil) {
			e.Enabled = true
			e.CooldownUntil = time.Time{}
		}
		// if endpoint is enabled, add to enabled list
		if e.Enabled && e.Client != nil {
			enabled = append(enabled, e)
		}
		// if endpoint is enabled and failover is enabled, add to failover list
		if e.Failover {
			failover = append(failover, e)
		}
	}
	// if enabled list is empty, return failover list
	if len(enabled) == 0 && len(failover) > 0 {
		l.WithField("failover", len(failover)).Info("using failover endpoints")
		return failover
	} else if len(enabled) == 0 && len(failover) == 0 && len(c.Endpoints) == 1 {
		l.WithField("failover", len(failover)).Info("using single endpoint")
		return c.Endpoints
	}
	sort.Slice(enabled, func(i, j int) bool {
		return enabled[i].BlockHead > enabled[j].BlockHead
	})
	var retEnabled []*ChainEndpoint
	hb := enabled[0].BlockHead
	l = l.WithField("head", hb)
	for _, e := range enabled {
		if hb >= e.BlockHead {
			l = l.WithField("endpoint", e.Endpoint)
			l.Info("adding endpoint to block-head enabled list")
			retEnabled = append(retEnabled, e)
		}
	}
	l.WithField("enabled", len(retEnabled)).Info("enabled endpoints")
	return retEnabled
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

func (c *chain) NextEndpoint(readOnly bool) (string, error) {
	l := log.WithFields(log.Fields{
		"chain":    c.Name,
		"action":   "NextEndpoint",
		"readOnly": readOnly,
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
	if readOnly {
		ts := enabled
		enabled = make([]*ChainEndpoint, 0)
		for _, e := range ts {
			if e.ReadOnly {
				enabled = append(enabled, e)
			}
		}
		if len(enabled) == 0 {
			l.Infof("no enabled read-only endpoints")
			enabled = ts
		}
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

func GetEndpoint(chainName string, readOnly bool) (string, error) {
	l := log.WithFields(log.Fields{
		"chain":    chainName,
		"action":   "GetEndpoint",
		"readOnly": readOnly,
	})
	l.Info("getting endpoint")
	for _, c := range Chains {
		if c.Name == chainName {
			ne, nerr := c.NextEndpoint(readOnly)
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

func (c *chain) UpdateEndpointBlockHead(ctx context.Context) error {
	l := log.WithFields(log.Fields{
		"chain":  c.Name,
		"action": "UpdateEndpointBlockHead",
	})
	l.Info("start")
	for _, e := range c.Endpoints {
		l = l.WithField("endpoint", e.Endpoint)
		if e.Client == nil {
			l.WithField("endpoint", e.Endpoint).Info("endpoint with no client")
			e.Enabled = false
		}
		if !e.Enabled {
			l.WithField("endpoint", e.Endpoint).Info("skipping disabled endpoint")
			metrics.EndpointEnabled.WithLabelValues(c.Name, e.Endpoint).Set(0)
			continue
		}
		bn, berr := e.Client.BlockNumber(ctx)
		if berr != nil {
			l.WithError(berr).Error("failed to get block number")
			// if we can't get the block number, we can't update the block head
			// put endpoint in cooldown
			if cerr := CooldownEndpoint(c.Name, e.Endpoint); cerr != nil {
				l.WithError(cerr).Error("failed to cooldown endpoint")
			}
			return berr
		}
		l = l.WithField("block", bn)
		if e.BlockHead != bn {
			e.BlockHead = bn
			l.Info("updated endpoint block head")
		} else {
			l.Info("endpoint block head unchanged")
		}
		metrics.EndpointBlockHead.WithLabelValues(c.Name, e.Endpoint).Set(float64(e.BlockHead))
		// if cooldown is zero, zero out metric
		if !e.CooldownUntil.IsZero() {
			metrics.Cooldowns.WithLabelValues(e.Endpoint).Set(float64(e.CooldownUntil.Unix()))
		} else if time.Until(e.CooldownUntil) <= 0 {
			metrics.Cooldowns.WithLabelValues(e.Endpoint).Set(0)
		}
		if e.Enabled {
			metrics.EndpointEnabled.WithLabelValues(c.Name, e.Endpoint).Set(1)
		} else {
			metrics.EndpointEnabled.WithLabelValues(c.Name, e.Endpoint).Set(0)
		}
	}
	l.Info("end")
	return nil
}

func updateBlockHeadWorker(ctx context.Context, chains chan *chain, res chan error) {
	l := log.WithFields(log.Fields{
		"action": "updateBlockHeadWorker",
	})
	l.Info("start")
	for c := range chains {
		l = l.WithField("chain", c.Name)
		if err := c.UpdateEndpointBlockHead(ctx); err != nil {
			l.WithError(err).Error("failed to update endpoint block head")
			res <- err
		} else {
			res <- nil
		}
	}
}

func UpdateChainBlockHeads(ctx context.Context) error {
	l := log.WithFields(log.Fields{
		"action": "UpdateChainBlockHeads",
	})
	l.Info("updating chain block heads")
	defer l.Info("updated chain block heads")
	chains := make(chan *chain, len(Chains))
	res := make(chan error, len(Chains))
	l = l.WithField("chains", len(Chains))
	l.Debug("starting update block heads workers")
	workers := 10
	if os.Getenv("UPDATE_BLOCK_HEADS_WORKERS") != "" {
		var err error
		workers, err = strconv.Atoi(os.Getenv("UPDATE_BLOCK_HEADS_WORKERS"))
		if err != nil {
			l.WithError(err).Error("failed to parse update block heads workers")
			return err
		}
	}
	for i := 0; i < workers; i++ {
		go updateBlockHeadWorker(ctx, chains, res)
	}
	l.Debug("started update block heads workers")
	for _, c := range Chains {
		l = l.WithField("chain", c.Name)
		l.Debug("sending chain to update block heads worker")
		chains <- c
	}
	l.Debug("sent chains to update block heads worker")
	close(chains)
	for i := 0; i < len(Chains); i++ {
		err := <-res
		if err != nil {
			l.WithError(err).Error("failed to update chain block heads")
		}
	}
	l.Debug("finished update block heads workers")
	return nil
}

func HealthProber() {
	l := log.WithFields(log.Fields{
		"action": "HealthProber",
	})
	l.Info("starting block head updater")
	defer l.Info("stopped block head updater")
	var probeInterval = time.Second * 10
	if os.Getenv("PROBE_INTERVAL") != "" {
		var err error
		probeInterval, err = time.ParseDuration(os.Getenv("PROBE_INTERVAL"))
		if err != nil {
			l.WithError(err).Error("failed to parse probe interval")
			return
		}
	}
	ctx := context.Background()
	for {
		time.Sleep(probeInterval)
		if err := UpdateChainBlockHeads(ctx); err != nil {
			l.WithError(err).Error("failed to update chain block heads")
		}
	}
}
