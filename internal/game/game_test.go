// SPDX-License-Identifier: AGPL-3.0-only

package game

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GolemMC/Golem/internal/world"
)

type testChunks struct {
	gate  chan struct{}
	calls atomic.Int64
}

type testPlayerStore struct {
	mu        sync.Mutex
	players   map[[16]byte]world.PlayerData
	saveErr   error
	saveCalls int
}

type testBlockStore struct {
	mu        sync.Mutex
	blocks    map[BlockPos]world.BlockState
	gate      chan struct{}
	started   chan struct{}
	startOnce sync.Once
	calls     int
}

func (s *testBlockStore) GetBlock(x, y, z int32) (world.BlockState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, exists := s.blocks[BlockPos{X: x, Y: y, Z: z}]
	if !exists {
		state = world.BlockState{Name: "minecraft:air"}
	}
	return state, nil
}

func (s *testBlockStore) SetBlock(x, y, z int32, state world.BlockState) (world.BlockState, error) {
	if s.started != nil {
		s.startOnce.Do(func() { close(s.started) })
	}
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	position := BlockPos{X: x, Y: y, Z: z}
	old := s.blocks[position]
	if old.Name == "" {
		old.Name = "minecraft:air"
	}
	s.blocks[position] = state
	s.calls++
	return old, nil
}

func (s *testBlockStore) PlaceBlock(x, y, z int32, state world.BlockState) (world.BlockState, error) {
	old, err := s.GetBlock(x, y, z)
	if err != nil {
		return world.BlockState{}, err
	}
	if old.Name != "minecraft:air" {
		return world.BlockState{}, errors.New("occupied")
	}
	return s.SetBlock(x, y, z, state)
}

func (s *testPlayerStore) LoadPlayer(id [16]byte) (world.PlayerData, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	player, exists := s.players[id]
	player.Exists = exists
	return player, nil
}

func (s *testPlayerStore) SavePlayer(_ context.Context, id [16]byte, player world.PlayerData) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.saveCalls++
	if s.saveErr != nil {
		return s.saveErr
	}
	player.Exists = true
	s.players[id] = player
	return nil
}

func (s *testChunks) LoadChunk(x, z int32) (world.Chunk, error) {
	s.calls.Add(1)
	if s.gate != nil {
		<-s.gate
	}
	return world.Chunk{X: x, Z: z, Sections: map[int8]world.ChunkSection{}, Heightmaps: map[string][]int64{}}, nil
}

func TestJoinDoesNotBlockOnChunkDiskIO(t *testing.T) {
	source := &testChunks{gate: make(chan struct{})}
	simulation, cancel := startTestGame(t, source)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	events := make(chan Event, 128)
	joined, _, err := simulation.Join(ctx, Player{ID: PlayerID{1}, Username: "one"}, events)
	if err != nil {
		t.Fatal(err)
	}
	if joined.EntityID == 0 || simulation.Online() != 1 {
		t.Fatalf("joined=%+v online=%d", joined, simulation.Online())
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for source.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if source.calls.Load() == 0 {
		t.Fatal("bounded worker pool did not start a chunk read")
	}
	close(source.gate)
}

func TestTypedMovementCommandsRemainOrdered(t *testing.T) {
	simulation, cancel := startTestGame(t, &testChunks{})
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	observerEvents := make(chan Event, 256)
	moverEvents := make(chan Event, 256)
	if _, _, err := simulation.Join(ctx, Player{ID: PlayerID{1}, Username: "observer"}, observerEvents); err != nil {
		t.Fatal(err)
	}
	if _, _, err := simulation.Join(ctx, Player{ID: PlayerID{2}, Username: "mover"}, moverEvents); err != nil {
		t.Fatal(err)
	}
	for _, x := range []float64{1, 2} {
		if err := simulation.Move(ctx, MovePlayer{PlayerID: PlayerID{2}, Position: Vec3{X: x, Y: 64}, Moved: true}); err != nil {
			t.Fatal(err)
		}
	}
	positions := make([]float64, 0, 2)
	for len(positions) < 2 {
		select {
		case event := <-observerEvents:
			if moved, ok := event.(PlayerMoved); ok && moved.Player.ID == (PlayerID{2}) {
				positions = append(positions, moved.Player.Position.X)
			}
		case <-ctx.Done():
			t.Fatalf("movement events=%v", positions)
		}
	}
	if positions[0] != 1 || positions[1] != 2 {
		t.Fatalf("movement order=%v", positions)
	}
}

func TestFullEventQueueRejectsJoin(t *testing.T) {
	simulation, cancel := startTestGame(t, &testChunks{})
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if _, _, err := simulation.Join(ctx, Player{ID: PlayerID{1}, Username: "slow"}, make(chan Event)); err == nil {
		t.Fatal("join with an unconsumed event queue succeeded")
	}
	if simulation.Online() != 0 {
		t.Fatalf("slow player remained online: %d", simulation.Online())
	}
}

func TestPlayerPersistenceLoadsAndAutosavesAuthoritativeState(t *testing.T) {
	id := PlayerID{1, 2, 3}
	store := &testPlayerStore{players: map[[16]byte]world.PlayerData{
		[16]byte(id): {
			Position: [3]float64{12.5, 70, -8.25}, Rotation: [2]float32{90, -15},
			SelectedHotbar: 4, Raw: map[string]any{"CustomField": "preserved"}, Exists: true,
		},
	}}
	simulation, cancel := startTestGameWithPlayers(t, &testChunks{}, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	joined, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256))
	if err != nil {
		t.Fatal(err)
	}
	if joined.Position != (Vec3{X: 12.5, Y: 70, Z: -8.25}) || joined.Rotation != (Rotation{Yaw: 90, Pitch: -15}) || joined.SelectedHotbar != 4 {
		t.Fatalf("join did not restore player data: %+v", joined)
	}
	if err := simulation.Move(ctx, MovePlayer{PlayerID: id, Position: Vec3{X: 20, Y: 72, Z: 5}, Rotation: Rotation{Yaw: 180, Pitch: 5}, Moved: true, Rotated: true}); err != nil {
		t.Fatal(err)
	}
	if err := simulation.SelectHotbar(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	if err := simulation.Save(ctx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	saved := store.players[[16]byte(id)]
	store.mu.Unlock()
	if saved.Position != ([3]float64{20, 72, 5}) || saved.Rotation != ([2]float32{180, 5}) || saved.SelectedHotbar != 7 {
		t.Fatalf("autosave wrote stale state: %+v", saved)
	}
	if saved.Raw["CustomField"] != "preserved" {
		t.Fatalf("autosave discarded unknown player NBT: %+v", saved.Raw)
	}
}

func TestCreativeInventoryClearPersistsAllPlayerSlots(t *testing.T) {
	id := PlayerID{4}
	store := &testPlayerStore{players: map[[16]byte]world.PlayerData{
		[16]byte(id): {
			Position: [3]float64{0.5, 64, 0.5},
			Inventory: []world.InventoryItem{
				{Slot: 0, ID: "minecraft:stone", Count: 64},
				{Slot: 9, ID: "minecraft:dirt", Count: 32},
				{Slot: 100, ID: "minecraft:diamond_boots", Count: 1},
				{Slot: -106, ID: "minecraft:shield", Count: 1},
			},
			Raw:    make(map[string]any),
			Exists: true,
		},
	}}
	simulation, cancel := startTestGameWithPlayers(t, &testChunks{}, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()

	joined, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder"}, make(chan Event, 256))
	if err != nil {
		t.Fatal(err)
	}
	if len(joined.Inventory) != 4 {
		t.Fatalf("loaded inventory=%+v", joined.Inventory)
	}

	inventory, err := simulation.ClearCreativeInventory(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory) != 0 {
		t.Fatalf("inventory after clear=%+v", inventory)
	}
	if err := simulation.Save(ctx); err != nil {
		t.Fatal(err)
	}

	store.mu.Lock()
	saved := store.players[[16]byte(id)]
	store.mu.Unlock()
	if len(saved.Inventory) != 0 {
		t.Fatalf("persisted inventory=%+v", saved.Inventory)
	}
	if err := simulation.Leave(ctx, id); err != nil {
		t.Fatal(err)
	}
	rejoined, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder"}, make(chan Event, 256))
	if err != nil {
		t.Fatal(err)
	}
	if len(rejoined.Inventory) != 0 {
		t.Fatalf("inventory after reconnect=%+v", rejoined.Inventory)
	}
}

func TestFailedLeaveSaveIsRetriedByFinalSave(t *testing.T) {
	id := PlayerID{9}
	store := &testPlayerStore{players: make(map[[16]byte]world.PlayerData), saveErr: errors.New("disk unavailable")}
	simulation, cancel := startTestGameWithPlayers(t, &testChunks{}, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256)); err != nil {
		t.Fatal(err)
	}
	if err := simulation.Move(ctx, MovePlayer{PlayerID: id, Position: Vec3{X: 3, Y: 65, Z: 4}, Moved: true}); err != nil {
		t.Fatal(err)
	}
	if err := simulation.Leave(ctx, id); err == nil {
		t.Fatal("leave reported a failed player save as successful")
	}
	store.mu.Lock()
	store.saveErr = nil
	store.mu.Unlock()
	if err := simulation.Save(ctx); err != nil {
		t.Fatal(err)
	}
	store.mu.Lock()
	saved := store.players[[16]byte(id)]
	calls := store.saveCalls
	store.mu.Unlock()
	if calls != 2 || saved.Position != ([3]float64{3, 65, 4}) {
		t.Fatalf("pending leave save was not retried: calls=%d player=%+v", calls, saved)
	}
}

func TestCreativeBlockPlacementAndBreakingArePersistedAndBroadcast(t *testing.T) {
	store := &testBlockStore{blocks: make(map[BlockPos]world.BlockState)}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	builderEvents := make(chan Event, 256)
	observerEvents := make(chan Event, 256)
	builder := PlayerID{1}
	if _, _, err := simulation.Join(ctx, Player{ID: builder, Username: "builder", Position: Vec3{Y: 64}}, builderEvents); err != nil {
		t.Fatal(err)
	}
	if _, _, err := simulation.Join(ctx, Player{ID: PlayerID{2}, Username: "observer", Position: Vec3{Y: 64}}, observerEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := simulation.SetCreativeInventorySlot(ctx, builder, 0, ItemStack{ID: "minecraft:stone", Count: 64}); err != nil {
		t.Fatal(err)
	}
	position := BlockPos{X: 1, Y: 64, Z: 1}
	placed, err := simulation.PlaceBlock(ctx, builder, position)
	if err != nil || placed.Err != nil || !placed.Applied || placed.State.Name != "minecraft:stone" {
		t.Fatalf("place result=%+v err=%v", placed, err)
	}
	waitForBlockEvent(t, ctx, observerEvents, position, "minecraft:stone")
	broken, err := simulation.BreakBlock(ctx, builder, position)
	if err != nil || broken.Err != nil || !broken.Applied || broken.State.Name != "minecraft:air" {
		t.Fatalf("break result=%+v err=%v", broken, err)
	}
	waitForBlockEvent(t, ctx, observerEvents, position, "minecraft:air")
	store.mu.Lock()
	persisted := store.blocks[position]
	calls := store.calls
	store.mu.Unlock()
	if calls != 2 || persisted.Name != "minecraft:air" {
		t.Fatalf("calls=%d persisted=%+v", calls, persisted)
	}
}

func TestBlockDiskIODoesNotBlockSimulationLoop(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	store := &testBlockStore{blocks: make(map[BlockPos]world.BlockState), gate: gate, started: started}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id := PlayerID{3}
	if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256)); err != nil {
		t.Fatal(err)
	}
	if _, err := simulation.SetCreativeInventorySlot(ctx, id, 0, ItemStack{ID: "minecraft:stone", Count: 1}); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		result, err := simulation.PlaceBlock(ctx, id, BlockPos{X: 1, Y: 64, Z: 1})
		if err == nil {
			err = result.Err
		}
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("block write did not reach the worker")
	}
	if err := simulation.Move(ctx, MovePlayer{PlayerID: id, Position: Vec3{X: 0.5, Y: 64, Z: 0.5}, Moved: true}); err != nil {
		t.Fatalf("movement blocked by disk write: %v", err)
	}
	close(gate)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestPendingBlockWriteIsRejectedWithAuthoritativeResync(t *testing.T) {
	gate := make(chan struct{})
	started := make(chan struct{})
	store := &testBlockStore{blocks: make(map[BlockPos]world.BlockState), gate: gate, started: started}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	position := BlockPos{X: 1, Y: 64, Z: 1}
	for index, id := range []PlayerID{{1}, {2}} {
		if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256)); err != nil {
			t.Fatal(err)
		}
		if _, err := simulation.SetCreativeInventorySlot(ctx, id, 0, ItemStack{ID: "minecraft:stone", Count: int32(index + 1)}); err != nil {
			t.Fatal(err)
		}
	}
	placed := make(chan BlockEditResult, 1)
	go func() {
		result, _ := simulation.PlaceBlock(ctx, PlayerID{1}, position)
		placed <- result
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("first block write did not reach the worker")
	}
	rejected := make(chan BlockEditResult, 1)
	if err := simulation.submit(ctx, ChangeBlock{PlayerID: PlayerID{2}, Position: position, State: world.BlockState{Name: "minecraft:air"}, reply: rejected}); err != nil {
		t.Fatal(err)
	}
	// Movement is queued after the rejected edit. Its reply proves the game loop
	// has registered the resynchronization waiter before the disk gate opens.
	if err := simulation.Move(ctx, MovePlayer{PlayerID: PlayerID{2}, Position: Vec3{X: 0.25, Y: 64}, Moved: true}); err != nil {
		t.Fatal(err)
	}
	close(gate)
	if result := <-placed; result.Err != nil || !result.Applied || result.State.Name != "minecraft:stone" {
		t.Fatalf("place result=%+v", result)
	}
	if result := <-rejected; result.Err == nil || result.Applied || result.State.Name != "minecraft:stone" {
		t.Fatalf("rejected result did not resync authoritative state: %+v", result)
	}
}

func waitForBlockEvent(t *testing.T, ctx context.Context, events <-chan Event, position BlockPos, name string) {
	t.Helper()
	for {
		select {
		case event := <-events:
			changed, ok := event.(BlockChanged)
			if ok && changed.Position == position && changed.State.Name == name {
				return
			}
		case <-ctx.Done():
			t.Fatalf("did not receive block update %s at %+v", name, position)
		}
	}
}

func startTestGame(t *testing.T, chunks ChunkSource) (*Game, context.CancelFunc) {
	return startTestGameWithPlayers(t, chunks, nil)
}

func startTestGameWithPlayers(t *testing.T, chunks ChunkSource, players PlayerStore) (*Game, context.CancelFunc) {
	return startTestGameWithStores(t, chunks, players, nil)
}

func startTestGameWithStores(t *testing.T, chunks ChunkSource, players PlayerStore, blocks BlockStore) (*Game, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	simulation := New(2, 2, chunks, players, blocks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go simulation.Run(ctx)
	return simulation, cancel
}
