// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/GolemMC/Golem/internal/auth"
	"github.com/GolemMC/Golem/internal/config"
	"github.com/GolemMC/Golem/internal/game"
	"github.com/GolemMC/Golem/internal/protocol"
	"github.com/GolemMC/Golem/internal/registry"
	"github.com/GolemMC/Golem/internal/version"
	"github.com/GolemMC/Golem/internal/world"
)

type missingChunks struct{}

func (missingChunks) LoadChunk(int32, int32) (world.Chunk, error) {
	return world.Chunk{}, world.ErrChunkMissing
}

type unusedVerifier struct{}

func (unusedVerifier) Verify(context.Context, string, string) (auth.Identity, error) {
	return auth.Identity{}, context.Canceled
}
func (unusedVerifier) Healthy() bool { return true }

type acceptingVerifier struct {
	identity auth.Identity
	hashSeen chan string
}

func (v acceptingVerifier) Verify(_ context.Context, username, hash string) (auth.Identity, error) {
	if username == v.identity.Username {
		v.hashSeen <- hash
	}
	return v.identity, nil
}
func (acceptingVerifier) Healthy() bool { return true }

func TestStatusPing(t *testing.T) {
	server, simulation, cancel := testServer(t)
	defer cancel()
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	guard := acquireTestGuard(server)
	done := make(chan error, 1)
	go func() {
		defer guard.release()
		done <- server.handle(context.Background(), serverConnection, guard)
	}()
	codec := protocol.FrameCodec{MaxPacketBytes: 2 << 20, CompressionThreshold: -1}
	writeHandshake(t, codec, clientConnection, version.ProtocolVersion, 1)
	if err := codec.Write(clientConnection, protocol.StatusServerboundRequest, nil); err != nil {
		t.Fatal(err)
	}
	id, payload, err := codec.Read(clientConnection)
	if err != nil || id != protocol.StatusClientboundResponse {
		t.Fatalf("status response id=%x err=%v", id, err)
	}
	decoder := protocol.NewDecoder(payload)
	text, err := decoder.String(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Description struct {
			Text string `json:"text"`
		} `json:"description"`
		Players struct {
			Online int `json:"online"`
		} `json:"players"`
	}
	if err := json.Unmarshal([]byte(text), &response); err != nil {
		t.Fatal(err)
	}
	if response.Description.Text != "Golem Experimental Server" || response.Players.Online != simulation.Online() {
		t.Fatalf("response=%+v", response)
	}
	var ping protocol.Encoder
	ping.Int64(42)
	if err := codec.Write(clientConnection, protocol.StatusServerboundPing, ping.Bytes()); err != nil {
		t.Fatal(err)
	}
	id, payload, err = codec.Read(clientConnection)
	if err != nil || id != protocol.StatusClientboundPong || len(payload) != 8 {
		t.Fatalf("pong id=%x bytes=%d err=%v", id, len(payload), err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestWrongProtocolGetsFriendlyLoginDisconnect(t *testing.T) {
	server, _, cancel := testServer(t)
	defer cancel()
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	guard := acquireTestGuard(server)
	done := make(chan error, 1)
	go func() {
		defer guard.release()
		done <- server.handle(context.Background(), serverConnection, guard)
	}()
	codec := protocol.FrameCodec{MaxPacketBytes: 2 << 20, CompressionThreshold: -1}
	writeHandshake(t, codec, clientConnection, 1, 2)
	id, payload, err := codec.Read(clientConnection)
	if err != nil || id != protocol.LoginClientboundDisconnect {
		t.Fatalf("disconnect id=%x err=%v", id, err)
	}
	decoder := protocol.NewDecoder(payload)
	reason, err := decoder.String(1024)
	if err != nil || !strings.Contains(reason, "1.21.1 only") {
		t.Fatalf("reason=%q err=%v", reason, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestSessionServerRejectsOfflineMode(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.OnlineMode = false
	simulation, cancel := startSimulationForTest()
	defer cancel()
	if _, err := New(Config{Server: cfg.Server, Auth: cfg.Auth, Network: cfg.Network}, Spawn{}, simulation, unusedVerifier{}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
		t.Fatal("offline session server was created")
	}
}

func TestAuthenticatedConfigurationReachesPlayLogin(t *testing.T) {
	server, _, cancelGame := testServer(t)
	defer cancelGame()
	serverConnection, clientConnection := net.Pipe()
	codec := protocol.FrameCodec{MaxPacketBytes: 2 << 20, CompressionThreshold: -1}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	identity := auth.Identity{UUID: [16]byte{1}, Username: "Player"}
	go func() { done <- server.configureAndPlay(ctx, serverConnection, codec, identity) }()
	id, _, err := codec.Read(clientConnection)
	if err != nil || id != protocol.ConfigClientboundSelectKnownPacks {
		t.Fatalf("known packs id=%x err=%v", id, err)
	}
	var packs protocol.Encoder
	packs.VarInt(1)
	packs.String(vanillaPackNamespace)
	packs.String(vanillaPackID)
	packs.String(vanillaPackVersion)
	if err := codec.Write(clientConnection, protocol.ConfigServerboundSelectKnownPacks, packs.Bytes()); err != nil {
		t.Fatal(err)
	}
	for {
		id, _, err = codec.Read(clientConnection)
		if err != nil {
			t.Fatal(err)
		}
		if id == protocol.ConfigClientboundFinish {
			break
		}
	}
	if err := codec.Write(clientConnection, protocol.ConfigServerboundFinish, nil); err != nil {
		t.Fatal(err)
	}
	var payload []byte
	for index := 0; index < 10; index++ {
		id, payload, err = codec.Read(clientConnection)
		if err != nil {
			t.Fatal(err)
		}
		if index == 0 && id != protocol.PlayClientboundLogin {
			t.Fatalf("first play packet=%x", id)
		}
		if id == protocol.PlayClientboundAbilities && (len(payload) != 9 || payload[0]&0x02 != 0) {
			t.Fatalf("spawn abilities incorrectly enable flying: %x", payload)
		}
	}
	cancel()
	_ = clientConnection.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("play session did not shut down")
	}
}

func TestProtocol767BlockInteractionDecoders(t *testing.T) {
	var action protocol.Encoder
	action.VarInt(0)
	action.Position(1, 64, -2)
	action.WriteByte(1)
	action.VarInt(42)
	kind, position, sequence, err := decodePlayerAction(action.Bytes())
	if err != nil || kind != 0 || position != (game.BlockPos{X: 1, Y: 64, Z: -2}) || sequence != 42 {
		t.Fatalf("player action kind=%d position=%+v sequence=%d err=%v", kind, position, sequence, err)
	}

	var placement protocol.Encoder
	placement.VarInt(0)
	placement.Position(1, 63, 1)
	placement.VarInt(1)
	placement.Float32(0.5)
	placement.Float32(1)
	placement.Float32(0.5)
	placement.Bool(false)
	placement.VarInt(7)
	target, sequence, mainHand, err := decodeUseItemOn(placement.Bytes())
	if err != nil || !mainHand || target != (game.BlockPos{X: 1, Y: 64, Z: 1}) || sequence != 7 {
		t.Fatalf("placement target=%+v sequence=%d main=%v err=%v", target, sequence, mainHand, err)
	}

	stone, exists, err := registry.ItemByName("minecraft:stone")
	if err != nil || !exists {
		t.Fatal("stone item is unavailable")
	}
	var creative protocol.Encoder
	creative.Int16(36)
	creative.VarInt(64)
	creative.VarInt(stone.ID)
	creative.VarInt(0)
	creative.VarInt(0)
	slot, item, err := decodeCreativeSlot(creative.Bytes())
	if err != nil || slot != 0 || item.ID != "minecraft:stone" || item.Count != 64 {
		t.Fatalf("creative slot=%d item=%+v err=%v", slot, item, err)
	}
}

func TestMandatoryOnlineLoginEncryptsAndVerifiesIdentity(t *testing.T) {
	identity := auth.Identity{UUID: [16]byte{7}, Username: "OnlinePlayer"}
	verifier := acceptingVerifier{identity: identity, hashSeen: make(chan string, 1)}
	server, _, cancelGame := testServerWithVerifier(t, verifier)
	defer cancelGame()
	serverConnection, clientConnection := net.Pipe()
	guard := acquireTestGuard(server)
	done := make(chan error, 1)
	go func() {
		defer guard.release()
		done <- server.handle(context.Background(), serverConnection, guard)
	}()
	codec := protocol.FrameCodec{MaxPacketBytes: 2 << 20, CompressionThreshold: -1}
	writeHandshake(t, codec, clientConnection, version.ProtocolVersion, 2)
	var start protocol.Encoder
	start.String(identity.Username)
	start.Write(make([]byte, 16))
	if err := codec.Write(clientConnection, protocol.LoginServerboundStart, start.Bytes()); err != nil {
		t.Fatal(err)
	}
	id, payload, err := codec.Read(clientConnection)
	if err != nil || id != protocol.LoginClientboundEncryptionRequest {
		t.Fatalf("encryption request id=%x err=%v", id, err)
	}
	decoder := protocol.NewDecoder(payload)
	if _, err := decoder.String(20); err != nil {
		t.Fatal(err)
	}
	publicDER, err := decoder.ByteArray(2048)
	if err != nil {
		t.Fatal(err)
	}
	token, err := decoder.ByteArray(32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decoder.Bool(); err != nil || decoder.Remaining() != 0 {
		t.Fatalf("malformed encryption request: %v", err)
	}
	parsed, err := x509.ParsePKIXPublicKey(publicDER)
	if err != nil {
		t.Fatal(err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("public key type %T", parsed)
	}
	secret := []byte("0123456789abcdef")
	encryptedSecret, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, secret)
	if err != nil {
		t.Fatal(err)
	}
	encryptedToken, err := rsa.EncryptPKCS1v15(rand.Reader, publicKey, token)
	if err != nil {
		t.Fatal(err)
	}
	var response protocol.Encoder
	response.ByteArray(encryptedSecret)
	response.ByteArray(encryptedToken)
	if err := codec.Write(clientConnection, protocol.LoginServerboundEncryptionResponse, response.Bytes()); err != nil {
		t.Fatal(err)
	}
	encrypter, err := auth.NewCFB8(secret, false)
	if err != nil {
		t.Fatal(err)
	}
	decrypter, err := auth.NewCFB8(secret, true)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := &encryptedConn{Conn: clientConnection, reader: cipher.StreamReader{S: decrypter, R: clientConnection}, writer: cipher.StreamWriter{S: encrypter, W: clientConnection}}
	id, _, err = codec.Read(encrypted)
	if err != nil || id != protocol.LoginClientboundSuccess {
		t.Fatalf("login success id=%x err=%v", id, err)
	}
	select {
	case hash := <-verifier.hashSeen:
		if hash == "" {
			t.Fatal("empty Mojang server hash")
		}
	case <-time.After(time.Second):
		t.Fatal("session verifier was not called")
	}
	if err := codec.Write(encrypted, protocol.LoginServerboundAcknowledged, nil); err != nil {
		t.Fatal(err)
	}
	id, _, err = codec.Read(encrypted)
	if err != nil || id != protocol.ConfigClientboundSelectKnownPacks {
		t.Fatalf("configuration did not begin id=%x err=%v", id, err)
	}
	_ = clientConnection.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("login handler did not stop")
	}
}

func TestFullOutboundQueueDisconnectsOnlySlowClient(t *testing.T) {
	server, _, cancelGame := testServer(t)
	defer cancelGame()
	serverConnection, clientConnection := net.Pipe()
	defer clientConnection.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	player := &playerSession{server: server, conn: serverConnection, ctx: ctx, cancel: cancel, out: make(chan outboundPacket, 1)}
	player.out <- outboundPacket{}
	if err := player.send(1, nil); err != ErrSlowClient {
		t.Fatalf("send error=%v", err)
	}
}

func testServer(t *testing.T) (*Server, *game.Game, context.CancelFunc) {
	return testServerWithVerifier(t, unusedVerifier{})
}

func testServerWithVerifier(t *testing.T, verifier auth.Verifier) (*Server, *game.Game, context.CancelFunc) {
	t.Helper()
	cfg := config.Defaults()
	cfg.Server.Favicon = ""
	simulation, cancel := startSimulationForTest()
	server, err := New(Config{Server: cfg.Server, Auth: cfg.Auth, Network: cfg.Network}, Spawn{Y: 64}, simulation, verifier, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	return server, simulation, cancel
}

func startSimulationForTest() (*game.Game, context.CancelFunc) {
	simulation := game.New(2, 2, missingChunks{}, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	go simulation.Run(ctx)
	return simulation, cancel
}

func acquireTestGuard(server *Server) *unauthGuard {
	server.unauth <- struct{}{}
	return &unauthGuard{slots: server.unauth}
}

func writeHandshake(t *testing.T, codec protocol.FrameCodec, connection net.Conn, protocolVersion, next int32) {
	t.Helper()
	var handshake protocol.Encoder
	handshake.VarInt(protocolVersion)
	handshake.String("localhost")
	handshake.Uint16(25565)
	handshake.VarInt(next)
	if err := codec.Write(connection, protocol.HandshakeServerboundIntention, handshake.Bytes()); err != nil {
		t.Fatal(err)
	}
}
