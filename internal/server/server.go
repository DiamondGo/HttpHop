package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"sync"
	"time"

	"github.com/DiamondGo/pollmux"
	"github.com/gorilla/mux"
	"go.uber.org/zap"
	"go.uber.org/zap/exp/zapslog"
	"golang.org/x/crypto/acme/autocert"

	"github.com/DiamondGo/HttpHop/internal/config"
	"github.com/DiamondGo/HttpHop/internal/registry"
	"github.com/DiamondGo/HttpHop/internal/router"
)

type Server struct {
	cfg          *config.ServerConfig
	registry     *registry.Registry
	sessionStore *pollmux.SessionStore
	routes       *router.RouteTable
	pollmuxCfg   pollmux.ServerConfig
	hooks        pollmux.Hooks
	clients      *ClientRegistry
	proxy        *httputil.ReverseProxy
	controlMux   *mux.Router
	certManager  *autocert.Manager
	stopSweeper  func()
	logger       *zap.Logger
	startedAt    time.Time

	httpSrv   *http.Server
	http80Srv *http.Server
	statusSrv *http.Server

	done     chan struct{}
	stopOnce sync.Once
}

func NewServer(cfg *config.ServerConfig, logger *zap.Logger) (*Server, error) {
	if err := config.ValidateServer(cfg); err != nil {
		return nil, err
	}

	reg := registry.NewRegistry()
	routes, err := buildRouteTable(cfg, reg)
	if err != nil {
		return nil, err
	}

	slogLogger := slog.New(zapslog.NewHandler(logger.Core()))
	pollmuxCfg := cfg.PollmuxServerConfig(slogLogger)
	pollmuxCfg.SessionIDFunc = func(r *http.Request) string {
		return mux.Vars(r)["id"]
	}

	s := &Server{
		cfg:          cfg,
		registry:     reg,
		sessionStore: pollmux.NewSessionStore(),
		routes:       routes,
		pollmuxCfg:   pollmuxCfg,
		clients:      NewClientRegistry(cfg.Clients),
		logger:       logger,
		done:         make(chan struct{}),
	}
	s.hooks = s.buildHooks()
	s.proxy = s.newProxy()
	s.controlMux = s.buildControlMux()
	return s, nil
}

func (s *Server) buildControlMux() *mux.Router {
	prefix := config.NormalizeControlPath(s.cfg.ControlPath)
	m := mux.NewRouter()
	m.Handle(prefix+"/connect", pollmux.ConnectHandler(s.sessionStore, s.pollmuxCfg, s.hooks)).Methods(http.MethodPost)
	m.Handle(prefix+"/{id}/poll", pollmux.PollHandler(s.sessionStore, s.pollmuxCfg, s.hooks)).Methods(http.MethodPost)
	m.Handle(prefix+"/{id}", pollmux.DeleteHandler(s.sessionStore, s.pollmuxCfg, s.hooks)).Methods(http.MethodDelete)
	return m
}

func (s *Server) buildHooks() pollmux.Hooks {
	return pollmux.Hooks{
		Authenticate: s.authenticateConnect,
		OnConnect:    s.onConnect,
		OnPoll:       s.onPoll,
		OnDisconnect: s.onDisconnect,
	}
}

func (s *Server) authenticateConnect(r *http.Request, req pollmux.ConnectRequest) (map[string]string, error) {
	token := bearerToken(r)
	if token == "" {
		return nil, pollmux.StatusErrorf(http.StatusUnauthorized, "invalid token")
	}

	clientID := ""
	if req.Meta != nil {
		clientID = req.Meta["client_id"]
	}
	if clientID == "" {
		return nil, pollmux.StatusErrorf(http.StatusBadRequest, "client_id is required in meta")
	}

	binding, ok := s.clients.Lookup(clientID)
	if !ok {
		return nil, pollmux.StatusErrorf(http.StatusForbidden, "unknown client_id")
	}
	if !validToken(binding.Token, token) {
		return nil, pollmux.StatusErrorf(http.StatusUnauthorized, "invalid token")
	}

	pool, _ := s.registry.Pool(binding.Subdomain)
	if pool != nil && pool.ByID(clientID) == nil && pool.Len() >= binding.MaxClients {
		return nil, pollmux.StatusErrorf(http.StatusConflict, "subdomain has reached max_clients")
	}

	return map[string]string{
		"subdomain": binding.Subdomain,
		"host_key":  binding.Subdomain,
		"client_id": clientID,
	}, nil
}

func (s *Server) onConnect(session *pollmux.Session, meta map[string]string) error {
	yamuxSess, err := pollmux.ClientSession(session)
	if err != nil {
		return err
	}

	clientID := meta["client_id"]
	binding, ok := s.clients.Lookup(clientID)
	if !ok {
		return fmt.Errorf("unknown client_id %q", clientID)
	}

	tun := &registry.ClientTunnel{
		ID:          clientID,
		Subdomain:   binding.Subdomain,
		Session:     session,
		Yamux:       yamuxSess,
		ConnectedAt: time.Now(),
		RemoteAddr:  remoteAddrFromSession(session),
	}

	if err := s.registry.Register(tun, binding.MaxClients); err != nil {
		_ = yamuxSess.Close()
		if errors.Is(err, registry.ErrPoolFull) {
			return pollmux.StatusErrorf(http.StatusConflict, "subdomain has reached max_clients")
		}
		return err
	}

	s.logger.Info("client connected",
		zap.String("subdomain", binding.Subdomain),
		zap.String("client_id", clientID),
		zap.String("session_id", session.ID))

	s.warmCert(binding.Subdomain)
	return nil
}

func (s *Server) onPoll(session *pollmux.Session, r *http.Request) {
	if h := r.Header.Get(pollmux.HeaderLocalHealth); h != "" {
		meta := session.Meta()
		sub := meta["subdomain"]
		cid := meta["client_id"]
		if sub != "" && cid != "" {
			s.registry.SetLocalHealth(sub, cid, h == "ok")
		}
	}
}

func (s *Server) onDisconnect(session *pollmux.Session, reason pollmux.DisconnectReason) {
	tun, ok := s.registry.GetBySessionID(session.ID)
	if ok {
		s.logger.Info("client disconnected",
			zap.String("subdomain", tun.Subdomain),
			zap.String("client_id", tun.ID),
			zap.String("session_id", session.ID),
			zap.String("reason", reason.String()))
		if tun.Yamux != nil {
			_ = tun.Yamux.Close()
		}
	}
	s.registry.RemoveBySessionID(session.ID)
}

func remoteAddrFromSession(_ *pollmux.Session) string {
	return ""
}

// Start begins listening. For tests, use StartOnListener with a custom listener.
func (s *Server) Start() error {
	s.startedAt = time.Now()
	s.stopSweeper = pollmux.StartSweeper(s.sessionStore, s.pollmuxCfg, s.hooks)

	handler := http.HandlerFunc(s.rootHandler)
	s.httpSrv = s.httpServer(handler)

	if s.cfg.TLS.Disable {
		s.logger.Info("starting HTTP server (TLS disabled)", zap.String("addr", s.publicListenAddr()))
		go func() {
			if err := s.httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				s.logger.Error("public server error", zap.Error(err))
			}
		}()
		if s.cfg.Status.Enabled && s.cfg.Status.Listen != "" {
			s.startStatusServer()
		}
		return nil
	}

	manager, err := s.setupTLS()
	if err != nil {
		return err
	}

	s.httpSrv.TLSConfig = manager.TLSConfig()
	s.logger.Info("starting HTTPS server", zap.String("addr", s.publicListenAddr()))
	go func() {
		if err := s.httpSrv.ListenAndServeTLS("", ""); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("public server error", zap.Error(err))
		}
	}()

	s.http80Srv = &http.Server{
		Addr:    s.cfg.HTTPListen,
		Handler: manager.HTTPHandler(httpsRedirectHandler("")),
	}
	go func() {
		if err := s.http80Srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("HTTP redirect server error", zap.Error(err))
		}
	}()

	if s.cfg.Status.Enabled && s.cfg.Status.Listen != "" {
		s.startStatusServer()
	}
	return nil
}

func (s *Server) startStatusServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("/status", s.handleStatus)
	s.statusSrv = &http.Server{Addr: s.cfg.Status.Listen, Handler: mux}
	go func() {
		if err := s.statusSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("status server error", zap.Error(err))
		}
	}()
}

// StartOnListener serves on a custom listener (for tests).
func (s *Server) StartOnListener(l net.Listener) error {
	s.startedAt = time.Now()
	s.stopSweeper = pollmux.StartSweeper(s.sessionStore, s.pollmuxCfg, s.hooks)
	s.httpSrv = s.httpServer(http.HandlerFunc(s.rootHandler))
	go func() {
		if err := s.httpSrv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.logger.Error("server error", zap.Error(err))
		}
	}()
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	var errOut error
	s.stopOnce.Do(func() {
		for _, sess := range s.sessionStore.All() {
			pollmux.CloseSession(s.sessionStore, s.hooks, sess, pollmux.ReasonServerClose)
		}
		if s.stopSweeper != nil {
			s.stopSweeper()
		}
		if s.httpSrv != nil {
			if err := s.httpSrv.Shutdown(ctx); err != nil {
				errOut = err
			}
		}
		if s.http80Srv != nil {
			_ = s.http80Srv.Shutdown(ctx)
		}
		if s.statusSrv != nil {
			_ = s.statusSrv.Shutdown(ctx)
		}
		close(s.done)
	})
	return errOut
}

func (s *Server) Registry() *registry.Registry { return s.registry }
func (s *Server) SessionStore() *pollmux.SessionStore { return s.sessionStore }
func (s *Server) Config() *config.ServerConfig { return s.cfg }

func (s *Server) RootHandler() http.HandlerFunc {
	return s.rootHandler
}

func (s *Server) Hooks() pollmux.Hooks {
	return s.hooks
}
