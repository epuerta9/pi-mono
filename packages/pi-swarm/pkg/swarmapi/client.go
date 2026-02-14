// Package swarmapi provides a public Go client for embedding Pi Swarm
// functionality into other applications. It wraps the internal stores
// with a clean, stable API surface.
package swarmapi

import (
	"context"
	"fmt"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	swarmctx "github.com/mariozechner/pi-mono/packages/pi-swarm/internal/context"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/memory"
	"github.com/mariozechner/pi-mono/packages/pi-swarm/internal/tasks"
)

// Client provides access to all swarm stores via a single connection.
type Client struct {
	nc      *nats.Conn
	js      jetstream.JetStream
	tasks   *tasks.Store
	memory  *memory.Store
	context *swarmctx.Store
	agentID string
}

// Connect creates a swarm client by connecting to an existing NATS
// server (either embedded or external).
func Connect(natsURL, agentID string) (*Client, error) {
	nc, err := nats.Connect(natsURL, nats.Name(agentID+"-client"))
	if err != nil {
		return nil, fmt.Errorf("connect to nats: %w", err)
	}

	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init jetstream: %w", err)
	}

	taskStore, err := tasks.NewStore(js, nc, agentID)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init task store: %w", err)
	}

	memStore, err := memory.NewStore(js, agentID)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init memory store: %w", err)
	}

	ctxStore, err := swarmctx.NewStore(js, agentID)
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("init context store: %w", err)
	}

	return &Client{
		nc:      nc,
		js:      js,
		tasks:   taskStore,
		memory:  memStore,
		context: ctxStore,
		agentID: agentID,
	}, nil
}

// Tasks returns the shared task store.
func (c *Client) Tasks() *tasks.Store { return c.tasks }

// Memory returns the shared memory store.
func (c *Client) Memory() *memory.Store { return c.memory }

// Context returns the shared context store.
func (c *Client) Context() *swarmctx.Store { return c.context }

// Close disconnects from NATS.
func (c *Client) Close() { c.nc.Close() }

// CreateTask is a convenience method for creating a task.
func (c *Client) CreateTask(ctx context.Context, title, description string, opts ...tasks.TaskOption) (*tasks.Task, error) {
	return c.tasks.Create(ctx, title, description, opts...)
}

// SetMemory is a convenience method for setting a global memory entry.
func (c *Client) SetMemory(ctx context.Context, name, content string) (*memory.MemoryEntry, error) {
	key := "memory.global." + name
	return c.memory.Put(ctx, key, content)
}
