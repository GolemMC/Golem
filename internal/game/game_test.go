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

	"github.com/GolemMC/Golem/internal/registry"
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
	old, err := s.PlaceBlocks([]world.BlockEdit{{X: x, Y: y, Z: z, State: state}})
	if err != nil {
		return world.BlockState{}, err
	}
	return old[0], nil
}

func (s *testBlockStore) PlaceBlocks(edits []world.BlockEdit) ([]world.BlockState, error) {
	if s.started != nil {
		s.startOnce.Do(func() { close(s.started) })
	}
	if s.gate != nil {
		<-s.gate
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := make([]world.BlockState, len(edits))
	for index, edit := range edits {
		state := s.blocks[BlockPos{X: edit.X, Y: edit.Y, Z: edit.Z}]
		if state.Name == "" {
			state.Name = "minecraft:air"
		}
		if state.Name != "minecraft:air" {
			return nil, errors.New("occupied")
		}
		old[index] = state
	}
	for _, edit := range edits {
		s.blocks[BlockPos{X: edit.X, Y: edit.Y, Z: edit.Z}] = edit.State
		s.calls++
	}
	return old, nil
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

func TestGrassBlockPlacementUsesValidDefaultState(t *testing.T) {
	store := &testBlockStore{blocks: make(map[BlockPos]world.BlockState)}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id := PlayerID{5}
	if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256)); err != nil {
		t.Fatal(err)
	}
	if _, err := simulation.SetCreativeInventorySlot(ctx, id, 0, ItemStack{ID: "minecraft:grass_block", Count: 1}); err != nil {
		t.Fatal(err)
	}
	position := BlockPos{X: 1, Y: 64, Z: 1}
	result, err := simulation.PlaceBlock(ctx, id, position)
	if err != nil || result.Err != nil || !result.Applied {
		t.Fatalf("place result=%+v err=%v", result, err)
	}
	if result.State.Name != "minecraft:grass_block" || result.State.Properties["snowy"] != "false" {
		t.Fatalf("grass state=%+v", result.State)
	}
	if _, err := registry.BlockStateID(result.State.Name, result.State.Properties); err != nil {
		t.Fatalf("invalid grass state: %v", err)
	}
}

func TestDoorPlacementCreatesBothHalves(t *testing.T) {
	store := &testBlockStore{blocks: make(map[BlockPos]world.BlockState)}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	observerEvents := make(chan Event, 256)
	id := PlayerID{6}
	if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}, Rotation: Rotation{Yaw: -90}}, make(chan Event, 256)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := simulation.Join(ctx, Player{ID: PlayerID{7}, Username: "observer", Position: Vec3{Y: 64}}, observerEvents); err != nil {
		t.Fatal(err)
	}
	if _, err := simulation.SetCreativeInventorySlot(ctx, id, 0, ItemStack{ID: "minecraft:oak_door", Count: 1}); err != nil {
		t.Fatal(err)
	}
	position := BlockPos{X: 1, Y: 64, Z: 1}
	result, err := simulation.PlaceBlock(ctx, id, position)
	if err != nil || result.Err != nil || !result.Applied || len(result.Updates) != 2 {
		t.Fatalf("place result=%+v err=%v", result, err)
	}
	wants := []struct {
		position BlockPos
		half     string
	}{
		{position: position, half: "lower"},
		{position: BlockPos{X: 1, Y: 65, Z: 1}, half: "upper"},
	}
	for index, want := range wants {
		update := result.Updates[index]
		if update.Position != want.position || update.State.Name != "minecraft:oak_door" || update.State.Properties["half"] != want.half || update.State.Properties["facing"] != "east" {
			t.Fatalf("update %d=%+v", index, update)
		}
		if _, err := registry.BlockStateID(update.State.Name, update.State.Properties); err != nil {
			t.Fatalf("invalid door state %d: %v", index, err)
		}
		waitForBlockEvent(t, ctx, observerEvents, want.position, "minecraft:oak_door")
	}
	store.mu.Lock()
	lower := store.blocks[position]
	upper := store.blocks[BlockPos{X: 1, Y: 65, Z: 1}]
	store.mu.Unlock()
	if lower.Properties["half"] != "lower" || upper.Properties["half"] != "upper" {
		t.Fatalf("persisted lower=%+v upper=%+v", lower, upper)
	}
}

func TestDoorPlacementLeavesBothTargetsUnchangedWhenUpperIsOccupied(t *testing.T) {
	upperPosition := BlockPos{X: 1, Y: 65, Z: 1}
	store := &testBlockStore{blocks: map[BlockPos]world.BlockState{upperPosition: {Name: "minecraft:stone"}}}
	simulation, cancel := startTestGameWithStores(t, &testChunks{}, nil, store)
	defer cancel()
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Second)
	defer stop()
	id := PlayerID{8}
	if _, _, err := simulation.Join(ctx, Player{ID: id, Username: "builder", Position: Vec3{Y: 64}}, make(chan Event, 256)); err != nil {
		t.Fatal(err)
	}
	if _, err := simulation.SetCreativeInventorySlot(ctx, id, 0, ItemStack{ID: "minecraft:iron_door", Count: 1}); err != nil {
		t.Fatal(err)
	}
	result, err := simulation.PlaceBlock(ctx, id, BlockPos{X: 1, Y: 64, Z: 1})
	if err != nil || result.Err == nil || result.Applied || len(result.Updates) != 2 {
		t.Fatalf("place result=%+v err=%v", result, err)
	}
	if result.Updates[0].State.Name != "minecraft:air" || result.Updates[1].State.Name != "minecraft:stone" {
		t.Fatalf("authoritative updates=%+v", result.Updates)
	}
	store.mu.Lock()
	lower, lowerExists := store.blocks[BlockPos{X: 1, Y: 64, Z: 1}]
	upper := store.blocks[upperPosition]
	store.mu.Unlock()
	if lowerExists || lower.Name != "" || upper.Name != "minecraft:stone" {
		t.Fatalf("lower=%+v exists=%v upper=%+v", lower, lowerExists, upper)
	}
}

func TestEveryDoorItemBuildsValidPlacementStates(t *testing.T) {
	doors := []string{
		"iron_door", "oak_door", "spruce_door", "birch_door", "jungle_door",
		"acacia_door", "cherry_door", "dark_oak_door", "mangrove_door", "bamboo_door",
		"crimson_door", "warped_door", "copper_door", "exposed_copper_door",
		"weathered_copper_door", "oxidized_copper_door", "waxed_copper_door",
		"waxed_exposed_copper_door", "waxed_weathered_copper_door", "waxed_oxidized_copper_door",
	}
	for _, name := range doors {
		definition, err := registry.DefaultBlockState("minecraft:" + name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		edits, err := placementEdits(BlockPos{X: 1, Y: 64, Z: 1}, 180, definition)
		if err != nil || len(edits) != 2 {
			t.Fatalf("%s edits=%+v err=%v", name, edits, err)
		}
		for _, edit := range edits {
			if edit.State.Properties["facing"] != "north" {
				t.Fatalf("%s facing=%q", name, edit.State.Properties["facing"])
			}
			if _, err := registry.BlockStateID(edit.State.Name, edit.State.Properties); err != nil {
				t.Fatalf("%s state=%+v: %v", name, edit.State, err)
			}
		}
	}
}

func TestHorizontalFacingRoundsPlayerYaw(t *testing.T) {
	tests := []struct {
		yaw  float32
		want string
	}{
		{yaw: 0, want: "south"},
		{yaw: 90, want: "west"},
		{yaw: 180, want: "north"},
		{yaw: -90, want: "east"},
		{yaw: 360, want: "south"},
	}
	for _, test := range tests {
		if got := horizontalFacing(test.yaw); got != test.want {
			t.Fatalf("yaw %v facing=%q want=%q", test.yaw, got, test.want)
		}
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
