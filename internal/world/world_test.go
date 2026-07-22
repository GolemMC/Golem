// SPDX-License-Identifier: AGPL-3.0-only

package world

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
)

func TestSaveWithReportsCoordinatedPersistenceFailure(t *testing.T) {
	w := &World{log: slog.New(slog.NewTextHandler(io.Discard, nil)), save: SaveState{Phase: SaveIdle}}
	want := errors.New("player data disk failure")
	if err := w.SaveWith(context.Background(), func(context.Context) error { return want }); !errors.Is(err, want) {
		t.Fatalf("SaveWith error=%v, want %v", err, want)
	}
	state := w.SaveState()
	if state.Phase != SaveFailed || state.LastError != want.Error() || !state.LastSuccess.IsZero() {
		t.Fatalf("failure state=%+v", state)
	}
}
