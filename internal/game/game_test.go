// SPDX-License-Identifier: AGPL-3.0-only

package game

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GolemMC/Golem/internal/world"
)

type testChunks struct {
	gate  chan struct{}
	calls atomic.Int64
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

func startTestGame(t *testing.T, chunks ChunkSource) (*Game, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	simulation := New(2, 2, chunks, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go simulation.Run(ctx)
	return simulation, cancel
}
