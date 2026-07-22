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
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/GolemMC/Golem/internal/registry"
	"github.com/GolemMC/Golem/internal/world"
)

const (
	TickInterval       = 50 * time.Millisecond
	commandCapacity    = 2048
	loadQueueCapacity  = 512
	playerIOCapacity   = 512
	blockIOCapacity    = 256
	loadCacheLimit     = 512
	maxPendingLoads    = 16384
	maxCommandsPerTick = 4096
)

type PlayerID [16]byte
type ChunkPos struct{ X, Z int32 }
type Vec3 struct{ X, Y, Z float64 }
type Rotation struct{ Yaw, Pitch float32 }
type BlockPos struct{ X, Y, Z int32 }

type ItemStack struct {
	Slot  int8
	ID    string
	Count int32
}

type Property struct {
	Name      string
	Value     string
	Signature string
}

type Player struct {
	ID             PlayerID
	Username       string
	Properties     []Property
	EntityID       int32
	Position       Vec3
	Rotation       Rotation
	OnGround       bool
	SelectedHotbar int32
	Inventory      []ItemStack
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

type BlockChanged struct {
	Position BlockPos
	State    world.BlockState
}

func (BlockChanged) gameEvent() {}

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
	data   world.PlayerData
	reply  chan joinReply
}

type LeavePlayer struct {
	PlayerID PlayerID
	reply    chan leaveReply
}

type SavePlayers struct{ reply chan []playerSave }

type SaveCompleted struct {
	PlayerID PlayerID
	sequence uint64
}

type SelectHotbar struct {
	PlayerID PlayerID
	Slot     int32
	reply    chan error
}

type SetCreativeSlot struct {
	PlayerID PlayerID
	Slot     int8
	Item     ItemStack
	reply    chan creativeSlotReply
}

type ClearCreativeInventory struct {
	PlayerID PlayerID
	reply    chan creativeSlotReply
}

type ChangeBlock struct {
	PlayerID PlayerID
	Position BlockPos
	State    world.BlockState
	Place    bool
	reply    chan BlockEditResult
}

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

func (JoinPlayer) gameCommand()             {}
func (LeavePlayer) gameCommand()            {}
func (SavePlayers) gameCommand()            {}
func (SaveCompleted) gameCommand()          {}
func (SelectHotbar) gameCommand()           {}
func (SetCreativeSlot) gameCommand()        {}
func (ClearCreativeInventory) gameCommand() {}
func (ChangeBlock) gameCommand()            {}
func (MovePlayer) gameCommand()             {}
func (SendChat) gameCommand()               {}
func (SubscribeChunks) gameCommand()        {}

type joinReply struct {
	self     Player
	existing []Player
	err      error
}

type creativeSlotReply struct {
	inventory []ItemStack
	err       error
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
	data        world.PlayerData
}

type ChunkSource interface {
	LoadChunk(chunkX, chunkZ int32) (world.Chunk, error)
}

type PlayerStore interface {
	LoadPlayer(id [16]byte) (world.PlayerData, error)
	SavePlayer(ctx context.Context, id [16]byte, player world.PlayerData) error
}

type BlockStore interface {
	GetBlock(x, y, z int32) (world.BlockState, error)
	SetBlock(x, y, z int32, state world.BlockState) (world.BlockState, error)
	PlaceBlocks(edits []world.BlockEdit) ([]world.BlockState, error)
}

type BlockEditResult struct {
	Position BlockPos
	State    world.BlockState
	Updates  []BlockChanged
	Applied  bool
	Err      error
}

type blockTask struct {
	playerID PlayerID
	position BlockPos
	state    world.BlockState
	edits    []world.BlockEdit
	place    bool
	readOnly bool
	rejected error
	reply    chan BlockEditResult
}

type blockResult struct {
	task    blockTask
	state   world.BlockState
	updates []BlockChanged
	err     error
}

type pendingBlockWrite struct {
	task    blockTask
	waiters []blockTask
}

type leaveReply struct {
	save *playerSave
	err  error
}

type playerSave struct {
	id       PlayerID
	data     world.PlayerData
	sequence uint64
}

type playerIOTask struct {
	ctx  context.Context
	load *playerLoadRequest
	save *playerSaveRequest
}

type playerLoadRequest struct {
	id    PlayerID
	reply chan playerLoadResult
}

type playerLoadResult struct {
	data world.PlayerData
	err  error
}

type playerSaveRequest struct {
	snapshot playerSave
	reply    chan error
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
	viewDistance     int32
	workers          int
	chunks           ChunkSource
	playerStore      PlayerStore
	blockStore       BlockStore
	log              *slog.Logger
	commands         chan command
	loadTasks        chan loadTask
	loadResults      chan loadResult
	playerIO         chan playerIOTask
	blockTasks       chan blockTask
	blockResults     chan blockResult
	players          map[PlayerID]*playerState
	nextEntityID     int32
	inflight         map[ChunkPos]struct{}
	waiting          map[ChunkPos]map[PlayerID]struct{}
	pending          []ChunkPos
	cache            map[ChunkPos]world.Chunk
	cacheOrder       []ChunkPos
	pendingSaves     map[PlayerID]playerSave
	nextSaveSequence uint64
	playerLocks      [64]sync.Mutex
	persistMu        sync.Mutex
	lastPersisted    map[PlayerID]uint64
	pendingBlocks    map[ChunkPos]*pendingBlockWrite
	online           atomic.Int64
	metricsMu        sync.RWMutex
	snapshot         Snapshot
	tickSamples      [100]time.Duration
	tickNext         int
	tickCount        int
}

func New(viewDistance, workers int, chunks ChunkSource, players PlayerStore, blocks BlockStore, log *slog.Logger) *Game {
	return &Game{
		viewDistance: int32(viewDistance), workers: workers, chunks: chunks, playerStore: players, blockStore: blocks, log: log,
		commands: make(chan command, commandCapacity), loadTasks: make(chan loadTask, loadQueueCapacity),
		loadResults: make(chan loadResult, loadQueueCapacity), playerIO: make(chan playerIOTask, playerIOCapacity),
		blockTasks: make(chan blockTask, blockIOCapacity), blockResults: make(chan blockResult, blockIOCapacity), players: make(map[PlayerID]*playerState),
		inflight: make(map[ChunkPos]struct{}), waiting: make(map[ChunkPos]map[PlayerID]struct{}), cache: make(map[ChunkPos]world.Chunk),
		pendingSaves: make(map[PlayerID]playerSave), lastPersisted: make(map[PlayerID]uint64), pendingBlocks: make(map[ChunkPos]*pendingBlockWrite),
	}
}

func (g *Game) Run(ctx context.Context) {
	for i := 0; i < g.workers; i++ {
		go g.chunkWorker(ctx)
		if g.playerStore != nil {
			go g.playerWorker(ctx)
		}
		if g.blockStore != nil {
			go g.blockWorker(ctx)
		}
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
	for i := 0; i < blockIOCapacity; i++ {
		select {
		case result := <-g.blockResults:
			g.handleBlockResult(result)
		default:
			i = blockIOCapacity
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

func (g *Game) blockWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-g.blockTasks:
			updates := make([]BlockChanged, 0, len(task.edits))
			var writeErr error
			if task.readOnly {
				for _, edit := range task.edits {
					state, err := g.blockStore.GetBlock(edit.X, edit.Y, edit.Z)
					if err != nil {
						writeErr = err
						break
					}
					updates = append(updates, BlockChanged{Position: BlockPos{X: edit.X, Y: edit.Y, Z: edit.Z}, State: state})
				}
				if writeErr == nil {
					writeErr = task.rejected
				}
			} else if task.place {
				_, writeErr = g.blockStore.PlaceBlocks(task.edits)
			} else {
				_, writeErr = g.blockStore.SetBlock(task.position.X, task.position.Y, task.position.Z, task.state)
			}
			if !task.readOnly {
				for _, edit := range task.edits {
					state := edit.State
					if writeErr != nil {
						var err error
						state, err = g.blockStore.GetBlock(edit.X, edit.Y, edit.Z)
						if err != nil {
							continue
						}
					}
					updates = append(updates, BlockChanged{Position: BlockPos{X: edit.X, Y: edit.Y, Z: edit.Z}, State: state})
				}
			}
			var state world.BlockState
			if len(updates) != 0 {
				state = updates[0].State
			}
			select {
			case <-ctx.Done():
				return
			case g.blockResults <- blockResult{task: task, state: state, updates: updates, err: writeErr}:
			}
		}
	}
}

func (g *Game) playerWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-g.playerIO:
			if task.load != nil {
				g.runPlayerLoad(task.ctx, task.load)
			}
			if task.save != nil {
				g.runPlayerSave(task.ctx, task.save)
			}
		}
	}
}

func (g *Game) runPlayerLoad(ctx context.Context, request *playerLoadRequest) {
	result := playerLoadResult{}
	if err := ctx.Err(); err != nil {
		result.err = err
	} else {
		lock := &g.playerLocks[int(request.id[0])%len(g.playerLocks)]
		lock.Lock()
		result.data, result.err = g.playerStore.LoadPlayer([16]byte(request.id))
		lock.Unlock()
	}
	request.reply <- result
}

func (g *Game) runPlayerSave(ctx context.Context, request *playerSaveRequest) {
	if err := ctx.Err(); err != nil {
		request.reply <- err
		return
	}
	snapshot := request.snapshot
	lock := &g.playerLocks[int(snapshot.id[0])%len(g.playerLocks)]
	lock.Lock()
	defer lock.Unlock()
	g.persistMu.Lock()
	last := g.lastPersisted[snapshot.id]
	g.persistMu.Unlock()
	if snapshot.sequence <= last {
		request.reply <- nil
		return
	}
	err := g.playerStore.SavePlayer(ctx, [16]byte(snapshot.id), snapshot.data)
	if err == nil {
		g.persistMu.Lock()
		if snapshot.sequence > g.lastPersisted[snapshot.id] {
			g.lastPersisted[snapshot.id] = snapshot.sequence
		}
		g.persistMu.Unlock()
	}
	request.reply <- err
}

func (g *Game) loadPlayer(ctx context.Context, id PlayerID) (world.PlayerData, error) {
	if g.playerStore == nil {
		return world.PlayerData{Raw: make(map[string]any)}, nil
	}
	reply := make(chan playerLoadResult, 1)
	task := playerIOTask{ctx: ctx, load: &playerLoadRequest{id: id, reply: reply}}
	select {
	case <-ctx.Done():
		return world.PlayerData{}, ctx.Err()
	case g.playerIO <- task:
	}
	select {
	case <-ctx.Done():
		return world.PlayerData{}, ctx.Err()
	case result := <-reply:
		return result.data, result.err
	}
}

func (g *Game) persistPlayer(ctx context.Context, snapshot playerSave) error {
	if g.playerStore == nil {
		return nil
	}
	reply := make(chan error, 1)
	task := playerIOTask{ctx: ctx, save: &playerSaveRequest{snapshot: snapshot, reply: reply}}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case g.playerIO <- task:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (g *Game) Join(ctx context.Context, player Player, events chan Event) (Player, []Player, error) {
	data, err := g.loadPlayer(ctx, player.ID)
	if err != nil {
		return Player{}, nil, fmt.Errorf("load player %s: %w", player.ID, err)
	}
	if data.Exists {
		position := Vec3{X: data.Position[0], Y: data.Position[1], Z: data.Position[2]}
		rotation := Rotation{Yaw: data.Rotation[0], Pitch: data.Rotation[1]}
		if !validPosition(position) || !validRotation(rotation) {
			return Player{}, nil, errors.New("saved player position or rotation is invalid")
		}
		player.Position = position
		player.Rotation = rotation
		player.SelectedHotbar = data.SelectedHotbar
	}
	player.Inventory = inventoryFromWorld(data.Inventory)
	reply := make(chan joinReply, 1)
	request := JoinPlayer{Player: player, Events: events, data: data, reply: reply}
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
	reply := make(chan leaveReply, 1)
	if err := g.submit(ctx, LeavePlayer{PlayerID: id, reply: reply}); err != nil {
		return err
	}
	var result leaveReply
	select {
	case <-ctx.Done():
		return ctx.Err()
	case result = <-reply:
	}
	if result.err != nil || result.save == nil {
		return result.err
	}
	if err := g.persistPlayer(ctx, *result.save); err != nil {
		return err
	}
	return g.submit(ctx, SaveCompleted{PlayerID: id, sequence: result.save.sequence})
}

func (g *Game) Save(ctx context.Context) error {
	if g.playerStore == nil {
		return nil
	}
	reply := make(chan []playerSave, 1)
	if err := g.submit(ctx, SavePlayers{reply: reply}); err != nil {
		return err
	}
	var snapshots []playerSave
	select {
	case <-ctx.Done():
		return ctx.Err()
	case snapshots = <-reply:
	}
	for _, snapshot := range snapshots {
		if err := g.persistPlayer(ctx, snapshot); err != nil {
			return fmt.Errorf("save player %s: %w", snapshot.id, err)
		}
		if err := g.submit(ctx, SaveCompleted{PlayerID: snapshot.id, sequence: snapshot.sequence}); err != nil {
			return err
		}
	}
	return nil
}

func (g *Game) SelectHotbar(ctx context.Context, id PlayerID, slot int32) error {
	reply := make(chan error, 1)
	if err := g.submit(ctx, SelectHotbar{PlayerID: id, Slot: slot, reply: reply}); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-reply:
		return err
	}
}

func (g *Game) SetCreativeInventorySlot(ctx context.Context, id PlayerID, slot int8, item ItemStack) ([]ItemStack, error) {
	reply := make(chan creativeSlotReply, 1)
	item.Slot = slot
	if err := g.submit(ctx, SetCreativeSlot{PlayerID: id, Slot: slot, Item: item, reply: reply}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-reply:
		return result.inventory, result.err
	}
}

func (g *Game) ClearCreativeInventory(ctx context.Context, id PlayerID) ([]ItemStack, error) {
	reply := make(chan creativeSlotReply, 1)
	if err := g.submit(ctx, ClearCreativeInventory{PlayerID: id, reply: reply}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-reply:
		return result.inventory, result.err
	}
}

func (g *Game) BreakBlock(ctx context.Context, id PlayerID, position BlockPos) (BlockEditResult, error) {
	return g.changeBlock(ctx, ChangeBlock{PlayerID: id, Position: position, State: world.BlockState{Name: "minecraft:air"}})
}

func (g *Game) PlaceBlock(ctx context.Context, id PlayerID, position BlockPos) (BlockEditResult, error) {
	return g.changeBlock(ctx, ChangeBlock{PlayerID: id, Position: position, Place: true})
}

func (g *Game) changeBlock(ctx context.Context, change ChangeBlock) (BlockEditResult, error) {
	change.reply = make(chan BlockEditResult, 1)
	if err := g.submit(ctx, change); err != nil {
		return BlockEditResult{}, err
	}
	select {
	case <-ctx.Done():
		return BlockEditResult{}, ctx.Err()
	case result := <-change.reply:
		return result, nil
	}
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
		snapshot := g.handleLeave(cmd.PlayerID)
		if cmd.reply != nil {
			cmd.reply <- leaveReply{save: snapshot}
		}
	case SavePlayers:
		cmd.reply <- g.playerSnapshots()
	case SaveCompleted:
		if pending, exists := g.pendingSaves[cmd.PlayerID]; exists && pending.sequence <= cmd.sequence {
			delete(g.pendingSaves, cmd.PlayerID)
		}
	case SelectHotbar:
		player := g.players[cmd.PlayerID]
		if player == nil {
			cmd.reply <- errors.New("player is not active")
		} else if cmd.Slot < 0 || cmd.Slot > 8 {
			cmd.reply <- errors.New("hotbar slot is outside 0..8")
		} else {
			player.player.SelectedHotbar = cmd.Slot
			player.data.SelectedHotbar = cmd.Slot
			cmd.reply <- nil
		}
	case SetCreativeSlot:
		g.handleCreativeSlot(cmd)
	case ClearCreativeInventory:
		g.handleClearCreativeInventory(cmd)
	case ChangeBlock:
		g.handleChangeBlock(cmd)
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

func (g *Game) handleCreativeSlot(cmd SetCreativeSlot) {
	player := g.players[cmd.PlayerID]
	if player == nil {
		cmd.reply <- creativeSlotReply{err: errors.New("player is not active")}
		return
	}
	if !validInventorySlot(cmd.Slot) {
		cmd.reply <- creativeSlotReply{err: fmt.Errorf("inventory slot %d is not editable", cmd.Slot)}
		return
	}
	if cmd.Item.Count < 0 || cmd.Item.Count > 99 || cmd.Item.Count > 0 && cmd.Item.ID == "" {
		cmd.reply <- creativeSlotReply{err: errors.New("creative item stack is invalid")}
		return
	}
	player.data.Inventory = setWorldInventoryItem(player.data.Inventory, cmd.Slot, cmd.Item)
	player.player.Inventory = inventoryFromWorld(player.data.Inventory)
	cmd.reply <- creativeSlotReply{inventory: append([]ItemStack(nil), player.player.Inventory...)}
}

func (g *Game) handleClearCreativeInventory(cmd ClearCreativeInventory) {
	player := g.players[cmd.PlayerID]
	if player == nil {
		cmd.reply <- creativeSlotReply{err: errors.New("player is not active")}
		return
	}
	player.data.Inventory = nil
	player.player.Inventory = nil
	cmd.reply <- creativeSlotReply{}
}

func (g *Game) handleChangeBlock(cmd ChangeBlock) {
	player := g.players[cmd.PlayerID]
	if player == nil {
		cmd.reply <- BlockEditResult{Position: cmd.Position, Err: errors.New("player is not active")}
		return
	}
	if g.blockStore == nil {
		cmd.reply <- BlockEditResult{Position: cmd.Position, Err: errors.New("block persistence is unavailable")}
		return
	}
	if !validBlockReach(player.player.Position, cmd.Position) {
		cmd.reply <- BlockEditResult{Position: cmd.Position, Err: errors.New("block is outside interaction range")}
		return
	}
	edits := []world.BlockEdit{{X: cmd.Position.X, Y: cmd.Position.Y, Z: cmd.Position.Z, State: cmd.State}}
	if cmd.Place {
		item, exists := selectedItem(player.player)
		if !exists {
			cmd.reply <- BlockEditResult{Position: cmd.Position, Err: errors.New("selected hotbar slot is empty")}
			return
		}
		definition, err := registry.DefaultBlockState(item.ID)
		if err != nil {
			cmd.reply <- BlockEditResult{Position: cmd.Position, Err: fmt.Errorf("selected item cannot be placed: %w", err)}
			return
		}
		edits, err = placementEdits(cmd.Position, player.player.Rotation.Yaw, definition)
		if err != nil {
			cmd.reply <- BlockEditResult{Position: cmd.Position, Err: err}
			return
		}
		cmd.State = edits[0].State
	}
	chunk := ChunkPos{X: cmd.Position.X >> 4, Z: cmd.Position.Z >> 4}
	if pending := g.pendingBlocks[chunk]; pending != nil {
		rejected := errors.New("chunk already has a pending block write")
		resync := blockTask{playerID: cmd.PlayerID, position: cmd.Position, edits: edits, readOnly: true, rejected: rejected, reply: cmd.reply}
		if cmd.Position == pending.task.position {
			pending.waiters = append(pending.waiters, resync)
			return
		}
		select {
		case g.blockTasks <- resync:
		default:
			cmd.reply <- BlockEditResult{Position: cmd.Position, Err: fmt.Errorf("%w; block read queue is full", rejected)}
		}
		return
	}
	task := blockTask{playerID: cmd.PlayerID, position: cmd.Position, state: cmd.State, edits: edits, place: cmd.Place, reply: cmd.reply}
	select {
	case g.blockTasks <- task:
		g.pendingBlocks[chunk] = &pendingBlockWrite{task: task}
	default:
		cmd.reply <- BlockEditResult{Position: cmd.Position, Err: errors.New("block write queue is full")}
	}
}

func (g *Game) handleBlockResult(result blockResult) {
	if result.task.readOnly {
		result.task.reply <- BlockEditResult{Position: result.task.position, State: result.state, Updates: result.updates, Err: result.err}
		return
	}
	chunk := ChunkPos{X: result.task.position.X >> 4, Z: result.task.position.Z >> 4}
	pending := g.pendingBlocks[chunk]
	delete(g.pendingBlocks, chunk)
	edit := BlockEditResult{Position: result.task.position, State: result.state, Updates: result.updates, Applied: result.err == nil, Err: result.err}
	if result.err == nil {
		g.invalidateChunk(chunk)
		for _, update := range result.updates {
			g.broadcast(update, result.task.playerID)
		}
	}
	result.task.reply <- edit
	if pending != nil {
		for _, waiter := range pending.waiters {
			select {
			case g.blockTasks <- waiter:
			default:
				waiter.reply <- BlockEditResult{Position: waiter.position, Err: fmt.Errorf("%w; block read queue is full", waiter.rejected)}
			}
		}
	}
}

func placementEdits(position BlockPos, yaw float32, definition registry.BlockStateDefinition) ([]world.BlockEdit, error) {
	if strings.HasSuffix(definition.Name, "_door") {
		properties := map[string]string{
			"facing":  horizontalFacing(yaw),
			"half":    "lower",
			"hinge":   "left",
			"open":    "false",
			"powered": "false",
		}
		lower := world.BlockEdit{X: position.X, Y: position.Y, Z: position.Z, State: world.BlockState{Name: definition.Name, Properties: properties}}
		upperProperties := make(map[string]string, len(properties))
		for key, value := range properties {
			upperProperties[key] = value
		}
		upperProperties["half"] = "upper"
		upper := world.BlockEdit{X: position.X, Y: position.Y + 1, Z: position.Z, State: world.BlockState{Name: definition.Name, Properties: upperProperties}}
		return []world.BlockEdit{lower, upper}, nil
	}
	if len(definition.Properties) != 0 && !isSnowyGround(definition.Properties) {
		return nil, errors.New("selected block needs unsupported placement rules")
	}
	state := world.BlockState{Name: definition.Name, Properties: definition.Properties}
	return []world.BlockEdit{{X: position.X, Y: position.Y, Z: position.Z, State: state}}, nil
}

func isSnowyGround(properties map[string]string) bool {
	return len(properties) == 1 && properties["snowy"] == "false"
}

func horizontalFacing(yaw float32) string {
	directions := [...]string{"south", "west", "north", "east"}
	if math.IsNaN(float64(yaw)) || math.IsInf(float64(yaw), 0) {
		return directions[0]
	}
	quarterTurn := int(math.Floor(float64(yaw)/90+0.5)) & 3
	return directions[quarterTurn]
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
	state := &playerState{player: clonePlayer(cmd.Player), events: cmd.Events, subscribed: make(map[ChunkPos]struct{}), data: cmd.data}
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

func (g *Game) handleLeave(id PlayerID) *playerSave {
	state := g.players[id]
	if state == nil {
		if pending, exists := g.pendingSaves[id]; exists {
			copy := pending
			return &copy
		}
		return nil
	}
	snapshot := g.newPlayerSave(state)
	g.pendingSaves[id] = snapshot
	delete(g.players, id)
	g.online.Add(-1)
	for position := range state.subscribed {
		if waiters := g.waiting[position]; waiters != nil {
			delete(waiters, id)
		}
	}
	g.broadcast(PlayerLeft{Player: clonePlayer(state.player)}, id)
	close(state.events)
	return &snapshot
}

func (g *Game) playerSnapshots() []playerSave {
	ids := make([]PlayerID, 0, len(g.pendingSaves)+len(g.players))
	for id := range g.pendingSaves {
		ids = append(ids, id)
	}
	for id := range g.players {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i].String() < ids[j].String() })
	snapshots := make([]playerSave, 0, len(ids))
	for _, id := range ids {
		if pending, exists := g.pendingSaves[id]; exists {
			snapshots = append(snapshots, pending)
			continue
		}
		if state := g.players[id]; state != nil {
			snapshots = append(snapshots, g.newPlayerSave(state))
		}
	}
	return snapshots
}

func (g *Game) newPlayerSave(state *playerState) playerSave {
	g.nextSaveSequence++
	data := state.data
	data.Position = [3]float64{state.player.Position.X, state.player.Position.Y, state.player.Position.Z}
	data.Rotation = [2]float32{state.player.Rotation.Yaw, state.player.Rotation.Pitch}
	data.SelectedHotbar = state.player.SelectedHotbar
	return playerSave{id: state.player.ID, data: data, sequence: g.nextSaveSequence}
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

func (g *Game) invalidateChunk(position ChunkPos) {
	delete(g.cache, position)
	for index, cached := range g.cacheOrder {
		if cached == position {
			g.cacheOrder = append(g.cacheOrder[:index], g.cacheOrder[index+1:]...)
			break
		}
	}
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

func validBlockReach(position Vec3, block BlockPos) bool {
	if block.Y < -64 || block.Y > 319 {
		return false
	}
	dx := float64(block.X) + 0.5 - position.X
	dy := float64(block.Y) + 0.5 - (position.Y + 1.62)
	dz := float64(block.Z) + 0.5 - position.Z
	return dx*dx+dy*dy+dz*dz <= 64
}

func validInventorySlot(slot int8) bool {
	return slot >= 0 && slot <= 35 || slot >= 100 && slot <= 103 || slot == -106
}

func selectedItem(player Player) (ItemStack, bool) {
	for _, item := range player.Inventory {
		if item.Slot == int8(player.SelectedHotbar) && item.Count > 0 {
			return item, true
		}
	}
	return ItemStack{}, false
}

func inventoryFromWorld(items []world.InventoryItem) []ItemStack {
	result := make([]ItemStack, 0, len(items))
	for _, item := range items {
		result = append(result, ItemStack{Slot: item.Slot, ID: item.ID, Count: item.Count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slot < result[j].Slot })
	return result
}

func setWorldInventoryItem(items []world.InventoryItem, slot int8, item ItemStack) []world.InventoryItem {
	result := make([]world.InventoryItem, 0, len(items)+1)
	for _, existing := range items {
		if existing.Slot != slot {
			result = append(result, existing)
		}
	}
	if item.Count > 0 {
		result = append(result, world.InventoryItem{Slot: slot, ID: item.ID, Count: item.Count})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Slot < result[j].Slot })
	return result
}

func clonePlayer(player Player) Player {
	player.Properties = append([]Property(nil), player.Properties...)
	player.Inventory = append([]ItemStack(nil), player.Inventory...)
	return player
}

func (p PlayerID) String() string { return fmt.Sprintf("%x", [16]byte(p)) }
