package node

import (
	"errors"
	"sync"
)

// LocalNodeWillStart is called right before local node start listening and
// handling messages. It can be used to add additional data to local node, set
// up NAT port mapping, etc.
type LocalNodeWillStart func(*LocalNode) bool

// LocalNodeStarted is called right after local node start listening and
// handling messages.
type LocalNodeStarted func(*LocalNode) bool

// RemoteNodeConnected is called when a connection is established with a remote
// node, but the remote node id is typically nil, so it's not a good time to use
// the node yet, but can be used to stop the connection to remote node. Returns
// if we should proceed to the next middleware.
type RemoteNodeConnected func(*RemoteNode) bool

// RemoteNodeReady is called when local node has received the node info from
// remote node and the remote node is ready to use. Returns if we should proceed
// to the next middleware.
type RemoteNodeReady func(*RemoteNode) bool

// RemoteNodeDisconnected is called when connection to remote node is closed.
// The cause of the connection close can be on either local node or remote node.
// Returns if we should proceed to the next middleware.
type RemoteNodeDisconnected func(*RemoteNode) bool

// middlewareStore stores the functions that will be called when certain events
// are triggered or in some pipeline
type middlewareStore struct {
	sync.RWMutex
	localNodeWillStart     []LocalNodeWillStart
	localNodeStarted       []LocalNodeStarted
	remoteNodeConnected    []RemoteNodeConnected
	remoteNodeReady        []RemoteNodeReady
	remoteNodeDisconnected []RemoteNodeDisconnected
}

// newMiddlewareStore creates a middlewareStore
func newMiddlewareStore() *middlewareStore {
	return &middlewareStore{
		localNodeWillStart:     make([]LocalNodeWillStart, 0),
		localNodeStarted:       make([]LocalNodeStarted, 0),
		remoteNodeConnected:    make([]RemoteNodeConnected, 0),
		remoteNodeReady:        make([]RemoteNodeReady, 0),
		remoteNodeDisconnected: make([]RemoteNodeDisconnected, 0),
	}
}

// ApplyMiddleware add a middleware to the store
func (store *middlewareStore) ApplyMiddleware(f interface{}) error {
	store.Lock()
	defer store.Unlock()

	switch f := f.(type) {
	case LocalNodeWillStart:
		if f == nil {
			return errors.New("middleware is nil")
		}
		store.localNodeWillStart = append(store.localNodeWillStart, f)
	case LocalNodeStarted:
		if f == nil {
			return errors.New("middleware is nil")
		}
		store.localNodeStarted = append(store.localNodeStarted, f)
	case RemoteNodeConnected:
		if f == nil {
			return errors.New("middleware is nil")
		}
		store.remoteNodeConnected = append(store.remoteNodeConnected, f)
	case RemoteNodeReady:
		if f == nil {
			return errors.New("middleware is nil")
		}
		store.remoteNodeReady = append(store.remoteNodeReady, f)
	case RemoteNodeDisconnected:
		if f == nil {
			return errors.New("middleware is nil")
		}
		store.remoteNodeDisconnected = append(store.remoteNodeDisconnected, f)
	default:
		return errors.New("unknown middleware type")
	}

	return nil
}
