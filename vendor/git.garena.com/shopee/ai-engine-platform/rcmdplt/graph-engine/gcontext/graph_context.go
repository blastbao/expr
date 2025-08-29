package gcontext

import (
	"context"
	"sync"

	"github.com/valyala/fastjson"
)

type IGraphEngineCtx interface {
	context.Context
	NodeParams(nodeName string) *fastjson.Value
	GetData(key string) (interface{}, bool)
	SetData(key string, val interface{})
	AddNodeParams(nodeName string, params *fastjson.Value)
	InnerContext() *context.Context
}

type GraphEngineCtx struct {
	context.Context

	// nodeParams is abtest node params.
	nodeParams map[string]*fastjson.Value

	// external data across the whole graph.
	externalData sync.Map
}

func NewGraphEngineCtx(ctx context.Context) *GraphEngineCtx {
	if ctx == nil {
		ctx = context.Background()
	}
	return &GraphEngineCtx{
		Context:    ctx,
		nodeParams: make(map[string]*fastjson.Value),
	}
}

func (c *GraphEngineCtx) GetData(key string) (interface{}, bool) {
	val, ok := c.externalData.Load(key)
	if !ok {
		return nil, false
	}
	return val, true
}

func (c *GraphEngineCtx) SetData(key string, val interface{}) {
	c.externalData.Store(key, val)
}

func (c *GraphEngineCtx) NodeParams(nodeName string) *fastjson.Value {
	return c.nodeParams[nodeName]
}

func (c *GraphEngineCtx) AddNodeParams(nodeName string, params *fastjson.Value) {
	c.nodeParams[nodeName] = params
}

func (c *GraphEngineCtx) InnerContext() *context.Context {
	return &c.Context
}
