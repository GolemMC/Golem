// SPDX-License-Identifier: AGPL-3.0-only

// Package session owns connected-client lifecycles, protocol state, timeouts,
// and bounded network queues. It submits typed commands to package game and
// never directly mutates authoritative gameplay or world state.
package session

import (
	"context"
	"crypto/cipher"
	"crypto/rsa"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image/png"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/GolemMC/Golem/internal/auth"
	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/game"
	"github.com/GolemMC/Golem/internal/protocol"
	"github.com/GolemMC/Golem/internal/version"
)

var ErrClosed = errors.New("Minecraft listener closed")

type Config struct {
	Server  config.Server
	Auth    config.Auth
	Network config.Network
}

type Spawn struct{ X, Y, Z int32 }

type statusWindow struct {
	start time.Time
	count int
}

type Server struct {
	cfg       Config
	spawn     Spawn
	game      *game.Game
	log       *slog.Logger
	verifier  auth.Verifier
	logins    *auth.ActiveLogins
	key       *rsa.PrivateKey
	publicKey []byte
	favicon   string
	listener  net.Listener
	unauth    chan struct{}
	connMu    sync.Mutex
	conns     map[net.Conn]struct{}
	closing   bool
	connWG    sync.WaitGroup
	statusMu  sync.Mutex
	statusIPs map[string]statusWindow
}

func New(cfg Config, spawn Spawn, simulation *game.Game, verifier auth.Verifier, log *slog.Logger) (*Server, error) {
	if !cfg.Auth.OnlineMode {
		return nil, errors.New("online authentication is mandatory")
	}
	if simulation == nil || verifier == nil {
		return nil, errors.New("session server requires game and authentication services")
	}
	key, publicKey, err := auth.GenerateKey()
	if err != nil {
		return nil, fmt.Errorf("generate login key: %w", err)
	}
	favicon, err := loadFavicon(cfg.Server.Favicon)
	if err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, spawn: spawn, game: simulation, log: log, verifier: verifier,
		logins: auth.NewActiveLogins(cfg.Server.MaxPlayers), key: key, publicKey: publicKey,
		favicon: favicon, unauth: make(chan struct{}, cfg.Network.MaxUnauthenticated),
		conns: make(map[net.Conn]struct{}), statusIPs: make(map[string]statusWindow),
	}, nil
}

func (s *Server) Online() int { return s.game.Online() }

func (s *Server) Addr() net.Addr {
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

func (s *Server) Listen(ctx context.Context) error {
	listener, err := net.Listen("tcp", net.JoinHostPort(s.cfg.Server.Address, fmt.Sprint(s.cfg.Server.Port)))
	if err != nil {
		return fmt.Errorf("listen for Minecraft connections: %w", err)
	}
	s.listener = listener
	go func() { <-ctx.Done(); _ = listener.Close() }()
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ErrClosed
			}
			var temporary net.Error
			if errors.As(err, &temporary) && temporary.Temporary() {
				continue
			}
			return err
		}
		select {
		case s.unauth <- struct{}{}:
			guard := &unauthGuard{slots: s.unauth}
			s.connMu.Lock()
			if s.closing {
				s.connMu.Unlock()
				guard.release()
				_ = connection.Close()
				continue
			}
			s.conns[connection] = struct{}{}
			s.connWG.Add(1)
			s.connMu.Unlock()
			go s.handleBoundary(ctx, connection, guard)
		default:
			_ = connection.Close()
			s.log.Warn("rejected connection: unauthenticated limit reached", "remote", connection.RemoteAddr())
		}
	}
}

func (s *Server) Close() error {
	s.connMu.Lock()
	s.closing = true
	s.connMu.Unlock()
	var listenerErr error
	if s.listener != nil {
		listenerErr = s.listener.Close()
	}
	s.connMu.Lock()
	for connection := range s.conns {
		_ = connection.Close()
	}
	s.connMu.Unlock()
	s.connWG.Wait()
	return listenerErr
}

type unauthGuard struct {
	once  sync.Once
	slots chan struct{}
}

func (g *unauthGuard) release() { g.once.Do(func() { <-g.slots }) }

func (s *Server) handleBoundary(ctx context.Context, raw net.Conn, guard *unauthGuard) {
	defer func() {
		guard.release()
		_ = raw.Close()
		s.connMu.Lock()
		delete(s.conns, raw)
		s.connMu.Unlock()
		s.connWG.Done()
		if recovered := recover(); recovered != nil {
			s.log.Error("panic in connection handler", "remote", raw.RemoteAddr(), "panic", recovered)
		}
	}()
	if err := s.handle(ctx, raw, guard); err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, context.Canceled) {
		s.log.Debug("connection closed", "remote", raw.RemoteAddr(), "error", err)
	}
}

func (s *Server) handle(ctx context.Context, connection net.Conn, guard *unauthGuard) error {
	_ = connection.SetDeadline(time.Now().Add(s.cfg.Auth.LoginTimeout.Duration))
	codec := protocol.FrameCodec{MaxPacketBytes: int(s.cfg.Network.MaxPacketBytes), CompressionThreshold: -1}
	id, payload, err := codec.Read(connection)
	if err != nil {
		return err
	}
	if id != protocol.HandshakeServerboundIntention {
		return fmt.Errorf("expected handshake packet, got 0x%x", id)
	}
	decoder := protocol.NewDecoder(payload)
	clientProtocol, err := decoder.VarInt()
	if err != nil {
		return err
	}
	if _, err := decoder.String(255); err != nil {
		return err
	}
	if _, err := decoder.Uint16(); err != nil {
		return err
	}
	next, err := decoder.VarInt()
	if err != nil {
		return err
	}
	if decoder.Remaining() != 0 {
		return fmt.Errorf("handshake contains %d trailing bytes", decoder.Remaining())
	}
	state, err := protocol.NextFromHandshake(next)
	if err != nil {
		return err
	}
	switch state {
	case protocol.StateStatus:
		if !s.allowStatus(connection.RemoteAddr()) {
			return errors.New("status request rate limit exceeded")
		}
		return s.status(connection, codec)
	case protocol.StateLogin:
		if clientProtocol != version.ProtocolVersion {
			s.log.Info("rejected unsupported Minecraft protocol", "client_protocol", clientProtocol, "supported_protocol", version.ProtocolVersion)
			_ = loginDisconnect(connection, codec, "Golem currently supports Minecraft Java Edition 1.21.1 only.")
			return nil
		}
		return s.login(ctx, connection, codec, guard)
	default:
		return fmt.Errorf("unsupported state %s", state)
	}
}

func (s *Server) allowStatus(address net.Addr) bool {
	host := address.String()
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	now := time.Now()
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	window := s.statusIPs[host]
	if window.start.IsZero() || now.Sub(window.start) >= time.Minute {
		window = statusWindow{start: now}
	}
	window.count++
	s.statusIPs[host] = window
	if len(s.statusIPs) > 1024 {
		for key, old := range s.statusIPs {
			if now.Sub(old.start) >= time.Minute {
				delete(s.statusIPs, key)
			}
		}
	}
	return window.count <= s.cfg.Network.StatusRequestsPerMinute
}

func (s *Server) status(connection net.Conn, codec protocol.FrameCodec) error {
	id, payload, err := codec.Read(connection)
	if err != nil {
		return err
	}
	if id != protocol.StatusServerboundRequest || len(payload) != 0 {
		return errors.New("invalid status request")
	}
	response := struct {
		Version struct {
			Name     string `json:"name"`
			Protocol int    `json:"protocol"`
		} `json:"version"`
		Players struct {
			Max    int `json:"max"`
			Online int `json:"online"`
		} `json:"players"`
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
		Favicon            string `json:"favicon,omitempty"`
		EnforcesSecureChat bool   `json:"enforcesSecureChat"`
	}{}
	response.Version.Name, response.Version.Protocol = version.MinecraftVersion, version.ProtocolVersion
	response.Players.Max, response.Players.Online = s.cfg.Server.MaxPlayers, s.game.Online()
	response.Description.Text, response.Favicon = s.cfg.Server.MOTD, s.favicon
	data, err := json.Marshal(response)
	if err != nil {
		return err
	}
	var encoded protocol.Encoder
	encoded.String(string(data))
	if err := codec.Write(connection, protocol.StatusClientboundResponse, encoded.Bytes()); err != nil {
		return err
	}
	id, payload, err = codec.Read(connection)
	if err != nil {
		return err
	}
	if id != protocol.StatusServerboundPing || len(payload) != 8 {
		return errors.New("invalid status ping")
	}
	return codec.Write(connection, protocol.StatusClientboundPong, payload)
}

func (s *Server) login(ctx context.Context, raw net.Conn, codec protocol.FrameCodec, guard *unauthGuard) error {
	id, payload, err := codec.Read(raw)
	if err != nil {
		return err
	}
	if id != protocol.LoginServerboundStart {
		return fmt.Errorf("expected login start, got 0x%x", id)
	}
	decoder := protocol.NewDecoder(payload)
	username, err := decoder.String(16)
	if err != nil {
		return err
	}
	if _, err := decoder.Bytes(16); err != nil {
		return fmt.Errorf("read proposed UUID: %w", err)
	}
	if decoder.Remaining() != 0 {
		return errors.New("login start has trailing data")
	}
	if !validUsername(username) {
		_ = loginDisconnect(raw, codec, "Invalid username")
		return nil
	}
	token, err := auth.NewVerifyToken()
	if err != nil {
		return err
	}
	var request protocol.Encoder
	request.String("")
	request.ByteArray(s.publicKey)
	request.ByteArray(token)
	request.Bool(true)
	if err := codec.Write(raw, protocol.LoginClientboundEncryptionRequest, request.Bytes()); err != nil {
		return err
	}
	id, payload, err = codec.Read(raw)
	if err != nil {
		return err
	}
	if id != protocol.LoginServerboundEncryptionResponse {
		return fmt.Errorf("expected encryption response, got 0x%x", id)
	}
	decoder = protocol.NewDecoder(payload)
	encryptedSecret, err := decoder.ByteArray(256)
	if err != nil {
		return err
	}
	encryptedToken, err := decoder.ByteArray(256)
	if err != nil {
		return err
	}
	if decoder.Remaining() != 0 {
		return errors.New("encryption response has trailing data")
	}
	secret, err := auth.Decrypt(s.key, encryptedSecret)
	if err != nil {
		return fmt.Errorf("decrypt shared secret: %w", err)
	}
	verifiedToken, err := auth.Decrypt(s.key, encryptedToken)
	if err != nil {
		return fmt.Errorf("decrypt verification token: %w", err)
	}
	if len(verifiedToken) != len(token) || subtle.ConstantTimeCompare(verifiedToken, token) != 1 {
		return errors.New("login verification token mismatch")
	}
	if len(secret) != 16 {
		return fmt.Errorf("shared secret is %d bytes; expected 16", len(secret))
	}
	encrypter, err := auth.NewCFB8(secret, false)
	if err != nil {
		return err
	}
	decrypter, err := auth.NewCFB8(secret, true)
	if err != nil {
		return err
	}
	connection := &encryptedConn{Conn: raw, reader: cipher.StreamReader{S: decrypter, R: raw}, writer: cipher.StreamWriter{S: encrypter, W: raw}}
	authContext, cancel := context.WithTimeout(ctx, s.cfg.Auth.LoginTimeout.Duration)
	defer cancel()
	identity, err := s.verifier.Verify(authContext, username, auth.ServerHash("", secret, s.publicKey))
	if err != nil {
		_ = loginDisconnect(connection, codec, "Authentication failed. Please try again.")
		s.log.Warn("online authentication failed", "username", username, "error", err)
		return err
	}
	if err := s.logins.Reserve(identity.UUID, identity.Username); err != nil {
		_ = loginDisconnect(connection, codec, err.Error())
		return nil
	}
	defer s.logins.Release(identity.UUID)
	guard.release()
	s.log.Info("online authentication succeeded", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID))
	var success protocol.Encoder
	success.UUID(identity.UUID)
	success.String(identity.Username)
	success.VarInt(int32(len(identity.Properties)))
	for _, property := range identity.Properties {
		success.String(property.Name)
		success.String(property.Value)
		success.Bool(property.Signature != "")
		if property.Signature != "" {
			success.String(property.Signature)
		}
	}
	success.Bool(false)
	if err := codec.Write(connection, protocol.LoginClientboundSuccess, success.Bytes()); err != nil {
		return err
	}
	id, payload, err = codec.Read(connection)
	if err != nil {
		return err
	}
	if id != protocol.LoginServerboundAcknowledged || len(payload) != 0 {
		return fmt.Errorf("expected login acknowledgement, got 0x%x", id)
	}
	return s.configureAndPlay(ctx, connection, codec, identity)
}

func loginDisconnect(writer io.Writer, codec protocol.FrameCodec, reason string) error {
	data, _ := json.Marshal(map[string]string{"text": reason})
	var encoded protocol.Encoder
	encoded.String(string(data))
	return codec.Write(writer, protocol.LoginClientboundDisconnect, encoded.Bytes())
}

type encryptedConn struct {
	net.Conn
	reader io.Reader
	writer io.Writer
}

func (c *encryptedConn) Read(data []byte) (int, error)  { return c.reader.Read(data) }
func (c *encryptedConn) Write(data []byte) (int, error) { return c.writer.Write(data) }

func validUsername(username string) bool {
	if len(username) < 1 || len(username) > 16 || !utf8.ValidString(username) {
		return false
	}
	for _, character := range username {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '_' {
			continue
		}
		return false
	}
	return true
}

func loadFavicon(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("open favicon: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > 128<<10 {
		return "", errors.New("favicon exceeds 128KiB")
	}
	configuration, err := png.DecodeConfig(file)
	if err != nil {
		return "", fmt.Errorf("decode favicon: %w", err)
	}
	if configuration.Width != 64 || configuration.Height != 64 {
		return "", errors.New("favicon must be exactly 64x64 pixels")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}

func IsClosed(err error) bool {
	return errors.Is(err, ErrClosed) || errors.Is(err, net.ErrClosed)
}
