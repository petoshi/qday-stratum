// Package bridge translates Sia Stratum jobs to QDAY's local mining API.
package bridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/petoshi/qday-stratum/internal/nodeapi"
)

const (
	maxRequestBytes   = 64 << 10
	maxRememberedJobs = 64
	maxMiners         = 128
	maxSubmissions    = 4096
)

type nodeClient interface {
	GetBlockTemplate(context.Context, string) (nodeapi.Template, error)
	SubmitBlock(context.Context, string) (string, error)
}

// Config controls the local Stratum listener and work refresh cadence.
type Config struct {
	ListenAddress string
	JobInterval   time.Duration
	Logger        *slog.Logger
}

// Server serves Sia Stratum and owns the current QDAY template lifecycle.
type Server struct {
	node nodeClient
	cfg  Config

	mu         sync.Mutex
	clients    map[*client]struct{}
	jobs       map[string]*job
	jobOrder   []string
	template   *workTemplate
	current    *job
	lastStamp  int64
	extra1Size uint8
	extra2Size uint8

	sequence      atomic.Uint64
	extraSequence atomic.Uint32
}

// NewServer constructs a solo bridge server.
func NewServer(node nodeClient, cfg Config) (*Server, error) {
	if node == nil {
		return nil, errors.New("QDAY node client is required")
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = "127.0.0.1:3333"
	}
	if cfg.JobInterval <= 0 {
		cfg.JobInterval = time.Second
	}
	if cfg.JobInterval < 250*time.Millisecond {
		return nil, errors.New("job interval cannot be below 250ms")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	server := &Server{
		node:    node,
		cfg:     cfg,
		clients: make(map[*client]struct{}),
		jobs:    make(map[string]*job),
	}
	var seed [4]byte
	if _, err := rand.Read(seed[:]); err == nil {
		server.extraSequence.Store(binary.LittleEndian.Uint32(seed[:]))
	}
	return server, nil
}

// Run listens until ctx is cancelled or the listener fails.
func (s *Server) Run(ctx context.Context) error {
	listener, err := net.Listen("tcp", s.cfg.ListenAddress)
	if err != nil {
		return err
	}
	return s.Serve(ctx, listener)
}

// Serve handles Sia Stratum connections on listener until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	defer listener.Close()
	s.cfg.Logger.Info("Sia Stratum ready", "listen", listener.Addr().String())

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-ctx.Done()
		listener.Close()
	}()
	go s.templateLoop(ctx)
	go s.refreshLoop(ctx)

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		c := newClient(s, conn)
		s.mu.Lock()
		if len(s.clients) >= maxMiners {
			s.mu.Unlock()
			conn.Close()
			s.cfg.Logger.Warn("miner limit reached", "remote", conn.RemoteAddr().String())
			continue
		}
		s.clients[c] = struct{}{}
		s.mu.Unlock()
		go c.run(ctx)
	}
}

func (s *Server) templateLoop(ctx context.Context) {
	var longPollID string
	for ctx.Err() == nil {
		template, err := s.node.GetBlockTemplate(ctx, longPollID)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.clearWork()
			s.cfg.Logger.Error("QDAY template unavailable", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			longPollID = ""
			continue
		}
		parsed, err := newWorkTemplate(template)
		if err != nil {
			s.clearWork()
			s.cfg.Logger.Error("rejected QDAY template", "error", err)
			longPollID = ""
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		longPollID = parsed.longPollID
		s.mu.Lock()
		s.template = parsed
		s.lastStamp = 0
		s.extra1Size, s.extra2Size = parsed.extra1Size, parsed.extra2Size
		s.mu.Unlock()
		s.publish(time.Now(), true)
	}
}

func (s *Server) clearWork() {
	s.mu.Lock()
	s.template = nil
	s.current = nil
	s.lastStamp = 0
	s.jobs = make(map[string]*job)
	s.jobOrder = nil
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		clients = append(clients, c)
	}
	s.mu.Unlock()
	for _, c := range clients {
		c.close()
	}
}

func (s *Server) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.JobInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.publish(now, false)
		}
	}
}

func (s *Server) publish(now time.Time, announce bool) {
	s.mu.Lock()
	template := s.template
	stamp := now.Unix()
	if template == nil {
		s.mu.Unlock()
		return
	}
	if stamp < template.timestamp {
		stamp = template.timestamp
	}
	if stamp == s.lastStamp && s.current != nil && s.current.longPollID == template.longPollID {
		s.mu.Unlock()
		return
	}
	sequence := s.sequence.Add(1)
	id := fmt.Sprintf("%x", sequence)
	next := template.newJob(id, sequence, time.Unix(stamp, 0))
	s.lastStamp = stamp
	s.current = next
	s.jobs[next.id] = next
	s.jobOrder = append(s.jobOrder, next.id)
	for len(s.jobOrder) > maxRememberedJobs {
		delete(s.jobs, s.jobOrder[0])
		s.jobOrder = s.jobOrder[1:]
	}
	clients := make([]*client, 0, len(s.clients))
	for c := range s.clients {
		if c.authorized.Load() {
			clients = append(clients, c)
		}
	}
	s.mu.Unlock()

	for _, c := range clients {
		if err := c.sendJob(next); err != nil {
			c.close()
		}
	}
	if announce {
		s.cfg.Logger.Info("new QDAY work", "height", next.height, "transactions", template.transactions, "job", next.id, "workers", len(clients), "difficulty", next.difficulty)
	} else {
		s.cfg.Logger.Debug("refreshed nonce space", "height", next.height, "job", next.id, "workers", len(clients))
	}
}

func (s *Server) remove(c *client) {
	s.mu.Lock()
	delete(s.clients, c)
	s.mu.Unlock()
}

func (s *Server) currentJob() *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.current
}

func (s *Server) lookupJob(id string) *job {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.jobs[id]
}

type rpcRequest struct {
	ID     any             `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

type rpcResponse struct {
	ID     any `json:"id"`
	Result any `json:"result"`
	Error  any `json:"error"`
}

func rpcFailure(code int, message string) []any { return []any{code, message, nil} }

type rpcNotification struct {
	ID     any    `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params"`
}

type client struct {
	server *Server
	conn   net.Conn
	write  sync.Mutex
	job    sync.Mutex
	once   sync.Once

	subscribed atomic.Bool
	authorized atomic.Bool
	worker     string
	lastDiff   float64
	lastJob    uint64
	extraNonce [4]byte

	submitMu sync.Mutex
	submits  map[string]struct{}
}

func newClient(server *Server, conn net.Conn) *client {
	c := &client{server: server, conn: conn, submits: make(map[string]struct{})}
	binary.LittleEndian.PutUint32(c.extraNonce[:], server.extraSequence.Add(1))
	return c
}

func (c *client) extraNonceBytes(size uint8) []byte {
	if size == 0 {
		return nil
	} else if size > uint8(len(c.extraNonce)) {
		return nil
	}
	return append([]byte(nil), c.extraNonce[:size]...)
}

func (c *client) close() {
	c.once.Do(func() {
		c.conn.Close()
		c.server.remove(c)
	})
}

func (c *client) run(ctx context.Context) {
	defer c.close()
	c.server.cfg.Logger.Info("miner connected", "remote", c.conn.RemoteAddr().String())
	scanner := bufio.NewScanner(c.conn)
	scanner.Buffer(make([]byte, 4096), maxRequestBytes)
	for scanner.Scan() {
		var request rpcRequest
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil || request.Method == "" {
			c.respond(nil, nil, rpcFailure(-32700, "invalid JSON-RPC request"))
			continue
		}
		c.handle(ctx, request)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, net.ErrClosed) {
		c.server.cfg.Logger.Info("miner disconnected", "worker", c.worker, "error", err)
	}
}

func decodeParams(raw json.RawMessage, result any) error {
	if len(raw) == 0 || string(raw) == "null" {
		raw = []byte("[]")
	}
	return json.Unmarshal(raw, result)
}

func (c *client) handle(ctx context.Context, request rpcRequest) {
	switch request.Method {
	case "mining.subscribe":
		subscription := strconv.FormatInt(time.Now().UnixNano(), 16)
		c.server.mu.Lock()
		extra1Size, extra2Size, ready := c.server.extra1Size, c.server.extra2Size, c.server.template != nil
		c.server.mu.Unlock()
		if !ready {
			c.respond(request.ID, nil, rpcFailure(20, "mining work is not ready; reconnect shortly"))
			return
		}
		c.subscribed.Store(true)
		c.respond(request.ID, []any{
			[]any{[]any{"mining.set_difficulty", subscription}, []any{"mining.notify", subscription}},
			hex.EncodeToString(c.extraNonceBytes(extra1Size)),
			extra2Size,
		}, nil)
	case "mining.authorize":
		if !c.subscribed.Load() {
			c.respond(request.ID, false, rpcFailure(25, "subscribe before authorization"))
			return
		}
		var params []string
		if err := decodeParams(request.Params, &params); err != nil || len(params) < 1 || strings.TrimSpace(params[0]) == "" || len(params[0]) > 256 {
			c.respond(request.ID, false, rpcFailure(20, "worker name is required"))
			return
		}
		c.worker = strings.TrimSpace(params[0])
		c.authorized.Store(true)
		c.respond(request.ID, true, nil)
		c.server.cfg.Logger.Info("miner authorized", "worker", c.worker, "remote", c.conn.RemoteAddr().String())
		if current := c.server.currentJob(); current != nil {
			if err := c.sendJob(current); err != nil {
				c.close()
			}
		}
	case "mining.configure":
		c.respond(request.ID, map[string]any{}, nil)
	case "mining.extranonce.subscribe", "mining.suggest_difficulty":
		c.respond(request.ID, true, nil)
	case "mining.submit":
		c.submit(ctx, request)
	default:
		c.respond(request.ID, nil, rpcFailure(-32601, "method not found"))
	}
}

func (c *client) send(value any) error {
	c.write.Lock()
	defer c.write.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	return json.NewEncoder(c.conn).Encode(value)
}

func (c *client) respond(id, result, failure any) {
	if err := c.send(rpcResponse{ID: id, Result: result, Error: failure}); err != nil {
		c.close()
	}
}

func (c *client) notify(method string, params any) error {
	return c.send(rpcNotification{ID: nil, Method: method, Params: params})
}

func (c *client) sendJob(j *job) error {
	c.job.Lock()
	defer c.job.Unlock()
	if j.sequence <= c.lastJob {
		return nil
	}
	if c.lastDiff != j.difficulty {
		if err := c.notify("mining.set_difficulty", []any{j.difficulty}); err != nil {
			return err
		}
		c.lastDiff = j.difficulty
	}
	if err := c.notify("mining.notify", j.notifyParams(true)); err != nil {
		return err
	}
	c.lastJob = j.sequence
	return nil
}

func (c *client) submit(ctx context.Context, request rpcRequest) {
	if !c.authorized.Load() {
		c.respond(request.ID, false, rpcFailure(24, "unauthorized worker"))
		return
	}
	var params []string
	if err := decodeParams(request.Params, &params); err != nil || len(params) != 5 {
		c.respond(request.ID, false, rpcFailure(20, "submit requires worker, job, extranonce2, time and nonce"))
		return
	}
	j := c.server.lookupJob(params[1])
	if j == nil {
		c.respond(request.ID, false, rpcFailure(21, "stale or unknown job"))
		return
	}
	key := strings.Join(params[1:], ":")
	c.submitMu.Lock()
	_, duplicate := c.submits[key]
	if !duplicate {
		if len(c.submits) >= maxSubmissions {
			c.submits = make(map[string]struct{})
		}
		c.submits[key] = struct{}{}
	}
	c.submitMu.Unlock()
	if duplicate {
		c.respond(request.ID, false, rpcFailure(22, "duplicate share"))
		return
	}
	block, hash, err := j.solve(hex.EncodeToString(c.extraNonceBytes(j.extra1Size)), params[2], params[3], params[4])
	if err != nil {
		c.respond(request.ID, false, rpcFailure(23, err.Error()))
		return
	}
	requestCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	blockID, err := c.server.node.SubmitBlock(requestCtx, hex.EncodeToString(block))
	if err != nil {
		c.server.cfg.Logger.Warn("QDAY rejected solved block", "worker", c.worker, "height", j.height, "hash", hex.EncodeToString(hash[:]), "error", err)
		c.respond(request.ID, false, rpcFailure(20, "QDAY node rejected block: "+err.Error()))
		return
	}
	c.server.cfg.Logger.Info("BLOCK ACCEPTED", "height", j.height, "worker", c.worker, "block", blockID)
	c.respond(request.ID, true, nil)
}
