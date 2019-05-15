package httpbackend

import (
	"crypto/tls"
	"fmt"
	"hash/fnv"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptrace"
	"sync/atomic"
	"time"

	"github.com/megaease/easegateway/pkg/common"
	"github.com/megaease/easegateway/pkg/context"
	"github.com/megaease/easegateway/pkg/logger"
	"github.com/megaease/easegateway/pkg/util/durationreadcloser"
	"github.com/megaease/easegateway/pkg/util/httpadaptor"
	"github.com/megaease/easegateway/pkg/util/httpheader"
	"github.com/megaease/easegateway/pkg/util/memorycache"
)

const (
	policyRoundRobin = "roundRobin"
	policyRandom     = "random"
	policyIPHash     = "ipHash"
	policyHeaderHash = "headerHash"
)

var (
	// All HTTPBackend instances use one globalClient in order to reuse
	// some resounces such as keepalive connections.
	globalClient = &http.Client{
		// NOTE: Timeout could be no limit, real client or server could cancel it.
		Timeout: 0,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 60 * time.Second,
				DualStack: true,
			}).DialContext,
			TLSClientConfig: &tls.Config{
				// NOTE: Could make it an paramenter,
				// when the requests need cross WAN.
				InsecureSkipVerify: true,
			},
			DisableCompression: false,
			// NOTE: The large number of Idle Connctions can
			// reduce overhead of building connections.
			MaxIdleConns:          10240,
			MaxIdleConnsPerHost:   512,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
)

func init() {
	rand.Seed(time.Now().UnixNano())
}

type (
	// ResponseGotFunc is the function type for
	// instantly calling back after getting real response.
	ResponseGotFunc = func(ctx context.HTTPContext)

	// HTTPBackend is the command HTTPBackend.
	HTTPBackend struct {
		spec *Spec

		servers     []Server
		codeCounter *codeCounter

		responseGotFuncs []ResponseGotFunc

		// NOTE: Will use its own client instead of globalClient,
		// if some arguments need to be exposed to admin.
		client      *http.Client
		count       uint64 // for roundRobin
		adaptor     *httpadaptor.HTTPAdaptor
		memoryCache *memorycache.MemoryCache
	}

	// Spec describes the HTTPBackend.
	Spec struct {
		V string `yaml:"-" v:"parent"`

		ServersTags []string          `yaml:"serversTags" v:"unique,dive,required"`
		Servers     []Server          `yaml:"servers" v:"required,dive"`
		LoadBalance *LoadBalance      `yaml:"loadBalance" v:"required"`
		Adaptor     *httpadaptor.Spec `yaml:"adaptor"`
		MemoryCache *memorycache.Spec `yaml:"memoryCache"`
	}

	// Server is backend server.
	Server struct {
		URL  string   `yaml:"url" v:"required,url"`
		Tags []string `yaml:"tags" v:"unique,dive,required"`
	}

	// LoadBalance is load balance for multiple servers.
	LoadBalance struct {
		V string `yaml:"-" v:"parent"`

		Policy        string `yaml:"policy" v:"required,oneof=roundRobin random ipHash headerHash"`
		HeaderHashKey string `yaml:"headerHashKey"`
	}
)

// Validate validates Spec.
func (s Spec) Validate() error {
	servers := s.pickServers()
	if len(servers) == 0 {
		return fmt.Errorf("serversTags picks none of servers")
	}

	return nil
}

// pickServers picks servers by serversTag.
func (s Spec) pickServers() []Server {
	var serverHasTag bool
	for _, server := range s.Servers {
		if len(server.Tags) != 0 {
			serverHasTag = true
			break
		}
	}
	if len(s.ServersTags) == 0 && !serverHasTag {
		return s.Servers
	}

	servers := make([]Server, 0)
	for _, server := range s.Servers {
		allFound := true
		for _, tag := range s.ServersTags {
			if !common.StrInSlice(tag, server.Tags) {
				allFound = false
				break
			}
		}
		if allFound {
			servers = append(servers, server)
		}
	}

	return servers
}

// Validate validates LoadBalance.
func (lb LoadBalance) Validate() error {
	if lb.Policy == policyHeaderHash && len(lb.HeaderHashKey) == 0 {
		return fmt.Errorf("headerHash needs to speficy headerHashKey")
	}

	return nil
}

// New creates a HTTPBackend.
func New(spec *Spec) *HTTPBackend {
	var adaptor *httpadaptor.HTTPAdaptor
	if spec.Adaptor != nil {
		adaptor = httpadaptor.New(spec.Adaptor)
	}
	var memoryCache *memorycache.MemoryCache
	if spec.MemoryCache != nil {
		memoryCache = memorycache.New(spec.MemoryCache)
	}

	servers := spec.pickServers()
	return &HTTPBackend{
		spec:        spec,
		servers:     servers,
		codeCounter: newCodeCounter(servers),
		client:      globalClient,
		adaptor:     adaptor,
		memoryCache: memoryCache,
	}
}

func (b *HTTPBackend) nextServer(ctx context.HTTPContext) *Server {
	switch b.spec.LoadBalance.Policy {
	case policyRoundRobin:
		return b.roundRobin(ctx)
	case policyRandom:
		return b.random(ctx)
	case policyIPHash:
		return b.ipHash(ctx)
	case policyHeaderHash:
		return b.headerHash(ctx)
	}

	logger.Errorf("BUG: unknown load balance policy: %s", b.spec.LoadBalance.Policy)

	return b.roundRobin(ctx)
}

func (b *HTTPBackend) roundRobin(ctx context.HTTPContext) *Server {
	count := atomic.AddUint64(&b.count, 1)
	return &b.servers[int(count)%len(b.servers)]
}

func (b *HTTPBackend) random(ctx context.HTTPContext) *Server {
	return &b.servers[rand.Intn(len(b.servers))]
}

func (b *HTTPBackend) hash32Once(key string) uint32 {
	hash := fnv.New32a()
	hash.Write([]byte(key))
	return hash.Sum32()
}
func (b *HTTPBackend) ipHash(ctx context.HTTPContext) *Server {
	sum32 := int(b.hash32Once(ctx.Request().RealIP()))
	return &b.servers[sum32%len(b.servers)]
}

func (b *HTTPBackend) headerHash(ctx context.HTTPContext) *Server {
	value := ctx.Request().Header().Get(b.spec.LoadBalance.HeaderHashKey)
	sum32 := int(b.hash32Once(value))
	return &b.servers[sum32%len(b.servers)]
}

func (b *HTTPBackend) adaptRequest(ctx context.HTTPContext, headerInPlace bool) (
	method, path string, header *httpheader.HTTPHeader) {
	r := ctx.Request()
	method, path, header = r.Method(), r.Path(), r.Header()
	if b.adaptor != nil {
		return b.adaptor.AdaptRequest(ctx, headerInPlace)
	}
	return
}

func (b *HTTPBackend) adaptResponse(ctx context.HTTPContext) {
	if b.adaptor != nil {
		b.adaptor.AdaptResponse(ctx)
	}
}

// Codes returns status codes.
func (b *HTTPBackend) Codes() map[string]map[int]uint64 {
	return b.codeCounter.codes()
}

// OnResponseGot registers ResponseGotFunc.
func (b *HTTPBackend) OnResponseGot(fn ResponseGotFunc) {
	b.responseGotFuncs = append(b.responseGotFuncs, fn)
}

// HandleWithResponse handles HTTPContext with returning response.
func (b *HTTPBackend) HandleWithResponse(ctx context.HTTPContext) {
	if b.memoryCache != nil {
		if b.memoryCache.Load(ctx) {
			return
		}
		defer b.memoryCache.Store(ctx)
	}

	r := ctx.Request()
	w := ctx.Response()

	server := b.nextServer(ctx)
	ctx.AddTag(fmt.Sprintf("backendAddr:%s", server.URL))

	method, path, header := b.adaptRequest(ctx, true /*headerInPlace*/)
	url := server.URL + path
	req, err := http.NewRequest(method, url, r.Body())
	if err != nil {
		logger.Errorf("BUG: new request failed: %v", err)
		w.SetStatusCode(http.StatusInternalServerError)
		ctx.AddTag(fmt.Sprintf("backendBug:%s", err.Error()))
		return
	}
	req.Header = header.Std()

	var (
		startTime     time.Time
		firstByteTime time.Time
	)
	trace := &httptrace.ClientTrace{
		GetConn: func(_ string) {
			startTime = time.Now()
		},
		GotFirstResponseByte: func() {
			firstByteTime = time.Now()
		},
	}
	req = req.WithContext(httptrace.WithClientTrace(ctx, trace))

	resp, err := b.client.Do(req)
	if err != nil {
		w.SetStatusCode(http.StatusServiceUnavailable)
		ctx.AddTag(fmt.Sprintf("backendErr:%s", err.Error()))
		return
	}
	b.codeCounter.count(server, resp.StatusCode)

	w.SetStatusCode(resp.StatusCode)
	ctx.AddTag(fmt.Sprintf("backendCode:%d", resp.StatusCode))
	w.Header().AddFromStd(resp.Header)
	body := durationreadcloser.New(resp.Body)
	w.SetBody(body)

	for _, fn := range b.responseGotFuncs {
		fn(ctx)
	}

	ctx.OnFinish(func() {
		totalDuration := firstByteTime.Sub(startTime) + body.Duration()
		ctx.AddTag(fmt.Sprintf("backendDuration:%v", totalDuration))
	})
}

// HandleWithoutResponse handles HTTPContext withou returning response.
func (b *HTTPBackend) HandleWithoutResponse(ctx context.HTTPContext) {
	r := ctx.Request()

	server := b.nextServer(ctx)
	ctx.AddTag(fmt.Sprintf("mirrorBackendAddr:%s", server.URL))

	method, path, header := b.adaptRequest(ctx, false /*headerInPlace*/)
	url := server.URL + path
	req, err := http.NewRequest(method, url, r.Body())
	if err != nil {
		logger.Errorf("BUG: new request failed: %v", err)
		return
	}
	req.Header = header.Std()

	resp, err := b.client.Do(req)
	if err != nil {
		ctx.AddTag(fmt.Sprintf("mirrorBackendFailed:%v", err))
		return
	}
	b.codeCounter.count(server, resp.StatusCode)

	go func() {
		// NOTE: Need to be read to completion and closed.
		// Reference: https://golang.org/pkg/net/http/#Response
		defer resp.Body.Close()
		io.Copy(ioutil.Discard, resp.Body)
	}()
}
