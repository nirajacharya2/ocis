// Package natsjsregistry implements a registry using natsjs kv store
package natsjsregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	natsjskv "github.com/go-micro/plugins/v4/store/nats-js-kv"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go"
	"go-micro.dev/v4/registry"
	"go-micro.dev/v4/store"
	"go-micro.dev/v4/util/cmd"
)

var (
	_registryName        = "nats-js-kv"
	_registryAddressEnv  = "MICRO_REGISTRY_ADDRESS"
	_registryUsernameEnv = "MICRO_REGISTRY_AUTH_USERNAME"
	_registryPasswordEnv = "MICRO_REGISTRY_AUTH_PASSWORD"

	_serviceDelimiter = "/"
)

func init() {
	cmd.DefaultRegistries[_registryName] = NewRegistry
}

// NewRegistry returns a new natsjs registry
func NewRegistry(opts ...registry.Option) registry.Registry {
	options := registry.Options{
		Context: context.Background(),
	}
	for _, o := range opts {
		o(&options)
	}
	exp, _ := options.Context.Value(expiryKey{}).(time.Duration)
	n := &storeregistry{
		opts:   options,
		typ:    _registryName,
		expiry: exp,
	}
	n.store = natsjskv.NewStore(n.storeOptions(options)...)
	return n
}

type storeregistry struct {
	opts   registry.Options
	store  store.Store
	typ    string
	expiry time.Duration
	lock   sync.RWMutex
}

// Init inits the registry
func (n *storeregistry) Init(opts ...registry.Option) error {
	n.lock.Lock()
	defer n.lock.Unlock()

	for _, o := range opts {
		o(&n.opts)
	}
	n.store = natsjskv.NewStore(n.storeOptions(n.opts)...)
	return n.store.Init(n.storeOptions(n.opts)...)
}

// Options returns the configured options
func (n *storeregistry) Options() registry.Options {
	return n.opts
}

// Register adds a service to the registry
func (n *storeregistry) Register(s *registry.Service, _ ...registry.RegisterOption) error {
	n.lock.RLock()
	defer n.lock.RUnlock()

	if s == nil {
		return errors.New("wont store nil service")
	}

	unique := uuid.New().String()
	if s.Metadata == nil {
		s.Metadata = make(map[string]string)
	}
	s.Metadata["uuid"] = unique

	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return n.store.Write(&store.Record{
		Key:    s.Name + _serviceDelimiter + unique,
		Value:  b,
		Expiry: n.expiry,
	})
}

// Deregister removes a service from the registry.
func (n *storeregistry) Deregister(s *registry.Service, _ ...registry.DeregisterOption) error {
	n.lock.RLock()
	defer n.lock.RUnlock()

	var unique string
	if s.Metadata != nil {
		unique = s.Metadata["uuid"]
	}

	return n.store.Delete(s.Name + _serviceDelimiter + unique)
}

// GetService gets a specific service from the registry
func (n *storeregistry) GetService(s string, _ ...registry.GetOption) ([]*registry.Service, error) {
	// avoid listing e.g. `webfinger` when requesting `web` by adding the delimiter to the service name
	return n.listServices(store.ListPrefix(s + _serviceDelimiter))
}

// ListServices lists all registered services
func (n *storeregistry) ListServices(...registry.ListOption) ([]*registry.Service, error) {
	return n.listServices()
}

// Watch allowes following the changes in the registry if it would be implemented
func (n *storeregistry) Watch(...registry.WatchOption) (registry.Watcher, error) {
	return NewWatcher(n)
}

// String returns the name of the registry
func (n *storeregistry) String() string {
	return n.typ
}

func (n *storeregistry) listServices(opts ...store.ListOption) ([]*registry.Service, error) {
	n.lock.RLock()
	defer n.lock.RUnlock()

	keys, err := n.store.List(opts...)
	if err != nil {
		return nil, err
	}

	svcs := make([]*registry.Service, 0, len(keys))
	for _, k := range keys {
		s, err := n.getService(k)
		if err != nil {
			// TODO: continue ?
			return nil, err
		}
		svcs = append(svcs, s)

	}
	return svcs, nil
}

func (n *storeregistry) getService(s string) (*registry.Service, error) {
	recs, err := n.store.Read(s)
	if err != nil {
		return nil, err
	}
	if len(recs) == 0 {
		return nil, registry.ErrNotFound
	}
	var svc registry.Service
	if err := json.Unmarshal(recs[0].Value, &svc); err != nil {
		return nil, err
	}
	return &svc, nil
}

func (n *storeregistry) storeOptions(opts registry.Options) []store.Option {
	storeoptions := []store.Option{
		store.Database("service-registry"),
		store.Table("service-registry"),
		natsjskv.DefaultMemory(),
		natsjskv.EncodeKeys(),
	}

	addr := []string{"127.0.0.1:9233"}
	if len(opts.Addrs) > 0 {
		addr = opts.Addrs
	} else if a := strings.Split(os.Getenv(_registryAddressEnv), ","); len(a) > 0 && a[0] != "" {
		addr = a
	}
	storeoptions = append(storeoptions, store.Nodes(addr...))

	natsOptions := nats.GetDefaultOptions()
	natsOptions.Name = "nats-js-kv-registry"
	natsOptions.User, natsOptions.Password = getAuth()
	natsOptions.ReconnectedCB = func(_ *nats.Conn) {
		if err := n.Init(); err != nil {
			fmt.Println("cannot reconnect to nats")
			os.Exit(1)
		}
	}
	natsOptions.ClosedCB = func(_ *nats.Conn) {
		fmt.Println("nats connection closed")
		os.Exit(1)
	}
	storeoptions = append(storeoptions, natsjskv.NatsOptions(natsOptions))

	if so, ok := opts.Context.Value(storeOptionsKey{}).([]store.Option); ok {
		storeoptions = append(storeoptions, so...)
	}

	return storeoptions
}

func getAuth() (string, string) {
	return os.Getenv(_registryUsernameEnv), os.Getenv(_registryPasswordEnv)
}
