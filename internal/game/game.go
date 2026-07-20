// SPDX-License-Identifier: AGPL-3.0-only

// Package game owns all live mutable gameplay state. Connections submit typed
// commands; only the simulation loop changes players or chunk subscriptions.
package game

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GolemMC/Golem/internal/world"
)

const (
	TickInterval       = 50 * time.Millisecond
	commandCapacity    = 2048
	loadQueueCapacity  = 512
	loadCacheLimit     = 512
	maxPendingLoads    = 16384
	maxCommandsPerTick = 4096
)

type PlayerID [16]byte
type ChunkPos struct{ X, Z int32 }
type Vec3 struct{ X, Y, Z float64 }
type Rotation struct{ Yaw, Pitch float32 }

type Property struct {
	Name      string
	Value     string
	Signature string
}

type Player struct {
	ID         PlayerID
	Username   string
	Properties []Property
	EntityID   int32
	Position   Vec3
	Rotation   Rotation
	OnGround   bool
}

type Event interface{ gameEvent() }

type PlayerJoined struct{ Player Player }

func (PlayerJoined) gameEvent() {}

type PlayerLeft struct{ Player Player }

func (PlayerLeft) gameEvent() {}

type PlayerMoved struct {
	Player   Player
	Previous Player
	Moved    bool
	Rotated  bool
}

func (PlayerMoved) gameEvent() {}

type ChatBroadcast struct {
	Sender  Player
	Message string
}

func (ChatBroadcast) gameEvent() {}

type ChunkLoaded struct {
	Position ChunkPos
	Chunk    world.Chunk
}

func (ChunkLoaded) gameEvent() {}

type ChunkUnavailable struct{ Position ChunkPos }

func (ChunkUnavailable) gameEvent() {}

type ChunkUnloaded struct{ Position ChunkPos }

func (ChunkUnloaded) gameEvent() {}

type ViewCenterChanged struct{ Center ChunkPos }

func (ViewCenterChanged) gameEvent() {}

type Notice struct{ Message string }

func (Notice) gameEvent() {}

type JoinPlayer struct {
	Player Player
	Events chan Event
	reply  chan joinReply
}

type LeavePlayer struct{ PlayerID PlayerID }

type MovePlayer struct {
	PlayerID PlayerID
	Position Vec3
	Rotation Rotation
	OnGround bool
	Moved    bool
	Rotated  bool
	reply    chan error
}

type SendChat struct {
	PlayerID PlayerID
	Message  string
	At       time.Time
	reply    chan error
}

type SubscribeChunks struct {
	PlayerID PlayerID
	Center   ChunkPos
}

type command interface{ gameCommand() }

func (JoinPlayer) gameCommand()      {}
func (LeavePlayer) gameCommand()     {}
func (MovePlayer) gameCommand()      {}
func (SendChat) gameCommand()        {}
func (SubscribeChunks) gameCommand() {}

type joinReply struct {
	self     Player
	existing []Player
	err      error
}

type playerState struct {
	player      Player
	events      chan Event
	center      ChunkPos
	subscribed  map[ChunkPos]struct{}
	chatWindow  time.Time
	chatCount   int
	lastChat    string
	chatRepeats int
}

type ChunkSource interface {
	LoadChunk(chunkX, chunkZ int32) (world.Chunk, error)
}

type loadTask struct{ position ChunkPos }
type loadResult struct {
	position ChunkPos
	chunk    world.Chunk
	err      error
}

type TickSnapshot struct {
	Last     time.Duration `json:"last"`
	Average  time.Duration `json:"average"`
	TPS      float64       `json:"tps"`
	Overruns uint64        `json:"overruns"`
	Ticks    uint64        `json:"ticks"`
}

type Snapshot struct {
	Players           int
	LoadedChunks      int
	PendingChunkLoads int
	Tick              TickSnapshot
}

type Game struct {
	viewDistance int32
	workers      int
	chunks       ChunkSource
	log          *slog.Logger
	commands     chan command
	loadTasks    chan loadTask
	loadResults  chan loadResult
	players      map[PlayerID]*playerState
	nextEntityID int32
	inflight     map[ChunkPos]struct{}
	waiting      map[ChunkPos]map[PlayerID]struct{}
	pending      []ChunkPos
	cache        map[ChunkPos]world.Chunk
	cacheOrder   []ChunkPos
	online       atomic.Int64
	metricsMu    sync.RWMutex
	snapshot     Snapshot
	tickSamples  [100]time.Duration
	tickNext     int
	tickCount    int
}

func New(viewDistance, workers int, chunks ChunkSource, log *slog.Logger) *Game {
	return &Game{
		viewDistance: int32(viewDistance), workers: workers, chunks: chunks, log: log,
		commands: make(chan command, commandCapacity), loadTasks: make(chan loadTask, loadQueueCapacity),
		loadResults: make(chan loadResult, loadQueueCapacity), players: make(map[PlayerID]*playerState),
		inflight: make(map[ChunkPos]struct{}), waiting: make(map[ChunkPos]map[PlayerID]struct{}), cache: make(map[ChunkPos]world.Chunk),
	}
}

func (g *Game) Run(ctx context.Context) {
	for i := 0; i < g.workers; i++ {
		go g.chunkWorker(ctx)
	}
	ticker := time.NewTicker(TickInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			g.processTick()
			g.recordTick(time.Since(start))
		}
	}
}

func (g *Game) processTick() {
	for i := 0; i < maxCommandsPerTick; i++ {
		select {
		case cmd := <-g.commands:
			g.handle(cmd)
		default:
			i = maxCommandsPerTick
		}
	}
	for i := 0; i < loadQueueCapacity; i++ {
		select {
		case result := <-g.loadResults:
			g.handleLoadResult(result)
		default:
			i = loadQueueCapacity
		}
	}
	for len(g.pending) > 0 {
		task := loadTask{position: g.pending[0]}
		select {
		case g.loadTasks <- task:
			g.pending = g.pending[1:]
		default:
			g.updateSnapshot()
			return
		}
	}
	g.updateSnapshot()
}

func (g *Game) chunkWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-g.loadTasks:
			chunk, err := g.chunks.LoadChunk(task.position.X, task.position.Z)
			result := loadResult{position: task.position, chunk: chunk, err: err}
			select {
			case <-ctx.Done():
				return
			case g.loadResults <- result:
			}
		}
	}
}

func (g *Game) Join(ctx context.Context, player Player, events chan Event) (Player, []Player, error) {
	reply := make(chan joinReply, 1)
	request := JoinPlayer{Player: player, Events: events, reply: reply}
	if err := g.submit(ctx, request); err != nil {
		return Player{}, nil, err
	}
	select {
	case <-ctx.Done():
		return Player{}, nil, ctx.Err()
	case result := <-reply:
		return result.self, result.existing, result.err
	}
}

func (g *Game) Leave(ctx context.Context, id PlayerID) error {
	return g.submit(ctx, LeavePlayer{PlayerID: id})
}

func (g *Game) Move(ctx context.Context, move MovePlayer) error {
	move.reply = make(chan error, 1)
	if err := g.submit(ctx, move); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-move.reply:
		return err
	}
}

func (g *Game) Chat(ctx context.Context, chat SendChat) error {
	chat.reply = make(chan error, 1)
	if chat.At.IsZero() {
		chat.At = time.Now()
	}
	if err := g.submit(ctx, chat); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-chat.reply:
		return err
	}
}

func (g *Game) submit(ctx context.Context, cmd command) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case g.commands <- cmd:
		return nil
	}
}

func (g *Game) Online() int { return int(g.online.Load()) }

func (g *Game) Snapshot() Snapshot {
	g.metricsMu.RLock()
	defer g.metricsMu.RUnlock()
	return g.snapshot
}

func (g *Game) handle(raw command) {
	switch cmd := raw.(type) {
	case JoinPlayer:
		g.handleJoin(cmd)
	case LeavePlayer:
		g.handleLeave(cmd.PlayerID)
	case MovePlayer:
		g.handleMove(cmd)
	case SendChat:
		g.handleChat(cmd)
	case SubscribeChunks:
		if player := g.players[cmd.PlayerID]; player != nil {
			g.updateSubscriptions(player, cmd.Center)
		}
	}
}

func (g *Game) handleJoin(cmd JoinPlayer) {
	if _, exists := g.players[cmd.Player.ID]; exists {
		cmd.reply <- joinReply{err: errors.New("player is already active")}
		return
	}
	g.nextEntityID++
	if g.nextEntityID <= 0 {
		cmd.reply <- joinReply{err: errors.New("entity ID space exhausted")}
		return
	}
	cmd.Player.EntityID = g.nextEntityID
	existing := make([]Player, 0, len(g.players))
	for _, other := range g.players {
		existing = append(existing, clonePlayer(other.player))
	}
	sort.Slice(existing, func(i, j int) bool { return existing[i].EntityID < existing[j].EntityID })
	state := &playerState{player: clonePlayer(cmd.Player), events: cmd.Events, subscribed: make(map[ChunkPos]struct{})}
	g.players[cmd.Player.ID] = state
	g.online.Add(1)
	g.broadcast(PlayerJoined{Player: clonePlayer(cmd.Player)}, cmd.Player.ID)
	center := ChunkPos{X: int32(math.Floor(cmd.Player.Position.X)) >> 4, Z: int32(math.Floor(cmd.Player.Position.Z)) >> 4}
	g.updateSubscriptions(state, center)
	if g.players[cmd.Player.ID] == nil {
		cmd.reply <- joinReply{err: errors.New("player event queue is full")}
		return
	}
	cmd.reply <- joinReply{self: clonePlayer(cmd.Player), existing: existing}
}

func (g *Game) handleLeave(id PlayerID) {
	state := g.players[id]
	if state == nil {
		return
	}
	delete(g.players, id)
	g.online.Add(-1)
	for position := range state.subscribed {
		if waiters := g.waiting[position]; waiters != nil {
			delete(waiters, id)
		}
	}
	g.broadcast(PlayerLeft{Player: clonePlayer(state.player)}, id)
	close(state.events)
}

func (g *Game) handleMove(cmd MovePlayer) {
	state := g.players[cmd.PlayerID]
	if state == nil {
		cmd.reply <- errors.New("player is not active")
		return
	}
	if cmd.Moved && !validPosition(cmd.Position) {
		cmd.reply <- errors.New("invalid player position")
		return
	}
	if cmd.Rotated && !validRotation(cmd.Rotation) {
		cmd.reply <- errors.New("invalid player rotation")
		return
	}
	previous := clonePlayer(state.player)
	if cmd.Moved {
		state.player.Position = cmd.Position
	}
	if cmd.Rotated {
		state.player.Rotation = cmd.Rotation
	}
	state.player.OnGround = cmd.OnGround
	next := clonePlayer(state.player)
	g.broadcast(PlayerMoved{Player: next, Previous: previous, Moved: cmd.Moved, Rotated: cmd.Rotated}, cmd.PlayerID)
	if cmd.Moved {
		center := ChunkPos{X: int32(math.Floor(next.Position.X)) >> 4, Z: int32(math.Floor(next.Position.Z)) >> 4}
		if center != state.center {
			g.updateSubscriptions(state, center)
		}
	}
	cmd.reply <- nil
}

func (g *Game) handleChat(cmd SendChat) {
	state := g.players[cmd.PlayerID]
	if state == nil {
		cmd.reply <- errors.New("player is not active")
		return
	}
	if !allowChat(state, cmd.Message, cmd.At) {
		if !g.emit(state, Notice{Message: "Chat rate limit exceeded."}) {
			g.handleLeave(cmd.PlayerID)
		}
		cmd.reply <- nil
		return
	}
	if len(cmd.Message) > 0 && cmd.Message[0] == '/' {
		if !g.emit(state, Notice{Message: "Commands are not supported."}) {
			g.handleLeave(cmd.PlayerID)
		}
		cmd.reply <- nil
		return
	}
	g.log.Info("chat", "username", state.player.Username, "message", cmd.Message)
	g.broadcast(ChatBroadcast{Sender: clonePlayer(state.player), Message: cmd.Message}, PlayerID{})
	cmd.reply <- nil
}

func allowChat(player *playerState, message string, now time.Time) bool {
	if player.chatWindow.IsZero() || now.Sub(player.chatWindow) >= 5*time.Second {
		player.chatWindow = now
		player.chatCount, player.chatRepeats = 0, 0
		player.lastChat = ""
	}
	if message == player.lastChat {
		player.chatRepeats++
	} else {
		player.lastChat, player.chatRepeats = message, 1
	}
	player.chatCount++
	return player.chatCount <= 5 && player.chatRepeats <= 2
}

func (g *Game) updateSubscriptions(player *playerState, center ChunkPos) {
	player.center = center
	if !g.emit(player, ViewCenterChanged{Center: center}) {
		g.handleLeave(player.player.ID)
		return
	}
	viewSize := int((2*g.viewDistance + 1) * (2*g.viewDistance + 1))
	wanted := make(map[ChunkPos]struct{}, viewSize)
	positions := make([]ChunkPos, 0, viewSize)
	for z := center.Z - g.viewDistance; z <= center.Z+g.viewDistance; z++ {
		for x := center.X - g.viewDistance; x <= center.X+g.viewDistance; x++ {
			position := ChunkPos{X: x, Z: z}
			wanted[position] = struct{}{}
			if _, exists := player.subscribed[position]; !exists {
				positions = append(positions, position)
			}
		}
	}
	for position := range player.subscribed {
		if _, keep := wanted[position]; keep {
			continue
		}
		delete(player.subscribed, position)
		if waiters := g.waiting[position]; waiters != nil {
			delete(waiters, player.player.ID)
		}
		if !g.emit(player, ChunkUnloaded{Position: position}) {
			g.handleLeave(player.player.ID)
			return
		}
	}
	sort.Slice(positions, func(i, j int) bool {
		dxi, dzi := positions[i].X-center.X, positions[i].Z-center.Z
		dxj, dzj := positions[j].X-center.X, positions[j].Z-center.Z
		return dxi*dxi+dzi*dzi < dxj*dxj+dzj*dzj
	})
	for _, position := range positions {
		player.subscribed[position] = struct{}{}
		if chunk, cached := g.cache[position]; cached {
			if !g.emit(player, ChunkLoaded{Position: position, Chunk: chunk}) {
				g.handleLeave(player.player.ID)
				return
			}
			continue
		}
		waiters := g.waiting[position]
		if waiters == nil {
			waiters = make(map[PlayerID]struct{})
			g.waiting[position] = waiters
		}
		waiters[player.player.ID] = struct{}{}
		if _, loading := g.inflight[position]; !loading {
			if len(g.inflight) >= maxPendingLoads {
				delete(waiters, player.player.ID)
				if len(waiters) == 0 {
					delete(g.waiting, position)
				}
				if !g.emit(player, ChunkUnavailable{Position: position}) {
					g.handleLeave(player.player.ID)
					return
				}
				continue
			}
			g.inflight[position] = struct{}{}
			g.pending = append(g.pending, position)
		}
	}
}

func (g *Game) handleLoadResult(result loadResult) {
	delete(g.inflight, result.position)
	waiters := g.waiting[result.position]
	delete(g.waiting, result.position)
	available := result.err == nil
	if available {
		g.cacheChunk(result.position, result.chunk)
	} else if !errors.Is(result.err, world.ErrChunkMissing) {
		g.log.Warn("chunk unavailable; clients will see read-only void", "chunk_x", result.position.X, "chunk_z", result.position.Z, "error", result.err)
	}
	var drop []PlayerID
	for id := range waiters {
		player := g.players[id]
		if player == nil {
			continue
		}
		if _, subscribed := player.subscribed[result.position]; !subscribed {
			continue
		}
		if available {
			if !g.emit(player, ChunkLoaded{Position: result.position, Chunk: result.chunk}) {
				drop = append(drop, id)
			}
		} else {
			if !g.emit(player, ChunkUnavailable{Position: result.position}) {
				drop = append(drop, id)
			}
		}
	}
	for _, id := range drop {
		g.handleLeave(id)
	}
}

func (g *Game) cacheChunk(position ChunkPos, chunk world.Chunk) {
	if _, exists := g.cache[position]; exists {
		return
	}
	if len(g.cacheOrder) >= loadCacheLimit {
		oldest := g.cacheOrder[0]
		g.cacheOrder = g.cacheOrder[1:]
		delete(g.cache, oldest)
	}
	g.cache[position] = chunk
	g.cacheOrder = append(g.cacheOrder, position)
}

func (g *Game) emit(player *playerState, event Event) bool {
	select {
	case player.events <- event:
		return true
	default:
		g.log.Warn("disconnecting slow player event consumer", "username", player.player.Username)
		return false
	}
}

func (g *Game) broadcast(event Event, except PlayerID) {
	var drop []PlayerID
	for id, player := range g.players {
		if except != (PlayerID{}) && id == except {
			continue
		}
		if !g.emit(player, event) {
			drop = append(drop, id)
		}
	}
	for _, id := range drop {
		g.handleLeave(id)
	}
}

func (g *Game) recordTick(duration time.Duration) {
	g.tickSamples[g.tickNext] = duration
	g.tickNext = (g.tickNext + 1) % len(g.tickSamples)
	if g.tickCount < len(g.tickSamples) {
		g.tickCount++
	}
	g.metricsMu.Lock()
	tick := g.snapshot.Tick
	tick.Last = duration
	tick.Ticks++
	if duration > TickInterval {
		tick.Overruns++
		g.log.Warn("tick overrun", "duration", duration, "target", TickInterval)
	}
	var total time.Duration
	for i := 0; i < g.tickCount; i++ {
		total += g.tickSamples[i]
	}
	if g.tickCount > 0 {
		tick.Average = total / time.Duration(g.tickCount)
	}
	tick.TPS = 20
	if duration > TickInterval {
		tick.TPS = float64(time.Second) / float64(duration)
	}
	g.snapshot.Tick = tick
	g.metricsMu.Unlock()
}

func (g *Game) updateSnapshot() {
	g.metricsMu.Lock()
	g.snapshot.Players = len(g.players)
	g.snapshot.LoadedChunks = len(g.cache)
	g.snapshot.PendingChunkLoads = len(g.inflight)
	g.metricsMu.Unlock()
}

func validPosition(position Vec3) bool {
	for _, value := range []float64{position.X, position.Y, position.Z} {
		if math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > 30_000_000 {
			return false
		}
	}
	return position.Y >= -2048 && position.Y <= 2048
}

func validRotation(rotation Rotation) bool {
	return !math.IsNaN(float64(rotation.Yaw)) && !math.IsInf(float64(rotation.Yaw), 0) &&
		!math.IsNaN(float64(rotation.Pitch)) && !math.IsInf(float64(rotation.Pitch), 0)
}

func clonePlayer(player Player) Player {
	player.Properties = append([]Property(nil), player.Properties...)
	return player
}

func (p PlayerID) String() string { return fmt.Sprintf("%x", [16]byte(p)) }
