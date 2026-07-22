// SPDX-License-Identifier: AGPL-3.0-only

package session

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"time"

	"github.com/GolemMC/Golem/internal/auth"
	"github.com/GolemMC/Golem/internal/game"
	"github.com/GolemMC/Golem/internal/protocol"
	"github.com/GolemMC/Golem/internal/registry"
	"github.com/GolemMC/Golem/internal/world"
)

const (
	vanillaPackNamespace    = "minecraft"
	vanillaPackID           = "core"
	vanillaPackVersion      = "1.21"
	maxConfigurationPackets = 64
	outboundCapacity        = 256
	gameEventCapacity       = 8192
)

var ErrSlowClient = errors.New("client outbound queue is full")

type outboundPacket struct {
	id      int32
	payload []byte
	done    chan error
}

type playerSession struct {
	server   *Server
	identity auth.Identity
	playerID game.PlayerID
	entityID int32
	conn     net.Conn
	codec    protocol.FrameCodec
	events   chan game.Event
	out      chan outboundPacket
	ctx      context.Context
	cancel   context.CancelFunc
}

func (s *Server) configureAndPlay(ctx context.Context, connection net.Conn, codec protocol.FrameCodec, identity auth.Identity) (err error) {
	stage := "known-pack request"
	configurationComplete := false
	joined := false
	defer func() {
		if err == nil || joined {
			return
		}
		s.log.Warn("player join failed", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID), "stage", stage, "error", err)
		reason := fmt.Sprintf("Join failed during %s. See the server log for details.", stage)
		if configurationComplete {
			_ = playDisconnect(connection, codec, reason)
		} else {
			_ = configurationDisconnect(connection, codec, reason)
		}
	}()

	var packs protocol.Encoder
	packs.VarInt(1)
	packs.String(vanillaPackNamespace)
	packs.String(vanillaPackID)
	packs.String(vanillaPackVersion)
	if err := codec.Write(connection, protocol.ConfigClientboundSelectKnownPacks, packs.Bytes()); err != nil {
		return err
	}
	stage = "known-pack response"
	acceptedCore, err := waitKnownPacks(connection, codec)
	if err != nil {
		return err
	}
	stage = "dynamic registries"
	registryPayloads, err := registry.ConfigurationPayloads(acceptedCore)
	if err != nil {
		return fmt.Errorf("prepare 1.21.1 registries: %w", err)
	}
	for _, payload := range registryPayloads {
		if err := codec.Write(connection, protocol.ConfigClientboundRegistryData, payload); err != nil {
			return err
		}
	}
	stage = "feature flags"
	var features protocol.Encoder
	features.VarInt(1)
	features.String("minecraft:vanilla")
	if err := codec.Write(connection, protocol.ConfigClientboundFeatureFlags, features.Bytes()); err != nil {
		return err
	}
	stage = "registry tags"
	if err := codec.Write(connection, protocol.ConfigClientboundTags, emptyRequiredTags()); err != nil {
		return err
	}
	stage = "configuration finish"
	if err := codec.Write(connection, protocol.ConfigClientboundFinish, nil); err != nil {
		return err
	}
	stage = "configuration acknowledgement"
	if err := waitConfigurationFinish(connection, codec); err != nil {
		return err
	}
	configurationComplete = true
	_ = connection.SetDeadline(time.Time{})

	properties := make([]game.Property, len(identity.Properties))
	for index, property := range identity.Properties {
		properties[index] = game.Property{Name: property.Name, Value: property.Value, Signature: property.Signature}
	}
	spawnY := s.spawn.Y
	if spawnY < -64 || spawnY > 319 {
		spawnY = 64
	}
	events := make(chan game.Event, gameEventCapacity)
	candidate := game.Player{
		ID: game.PlayerID(identity.UUID), Username: identity.Username, Properties: properties,
		Position: game.Vec3{X: float64(s.spawn.X) + 0.5, Y: float64(spawnY), Z: float64(s.spawn.Z) + 0.5},
	}
	stage = "game join"
	self, existing, err := s.game.Join(ctx, candidate, events)
	if err != nil {
		return err
	}
	defer func() {
		leaveContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if leaveErr := s.game.Leave(leaveContext, self.ID); leaveErr != nil {
			s.log.Error("player save on disconnect failed", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID), "error", leaveErr)
		}
		cancel()
	}()
	stage = "initial play data"
	if err := s.sendInitialPlay(connection, codec, self, existing); err != nil {
		return err
	}
	player := newPlayerSession(ctx, s, identity, self, connection, codec, events)
	player.startWriter()
	player.startEventWriter()
	joined = true
	s.log.Info("player joined", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID))
	defer s.log.Info("player left", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID))
	playErr := s.playLoop(ctx, player)
	if playErr != nil && !errors.Is(playErr, io.EOF) && !errors.Is(playErr, net.ErrClosed) && !errors.Is(playErr, context.Canceled) {
		s.log.Warn("player session failed", "username", identity.Username, "uuid", auth.FormatUUID(identity.UUID), "error", playErr)
		if payload, encodeErr := textComponent("Session ended because of a server protocol error. See the server log."); encodeErr == nil {
			_ = player.sendSync(protocol.PlayClientboundDisconnect, payload)
		}
	}
	player.cancel()
	_ = connection.Close()
	return playErr
}

func waitKnownPacks(connection net.Conn, codec protocol.FrameCodec) (bool, error) {
	for range maxConfigurationPackets {
		id, payload, err := codec.Read(connection)
		if err != nil {
			return false, err
		}
		if id != protocol.ConfigServerboundSelectKnownPacks {
			continue
		}
		decoder := protocol.NewDecoder(payload)
		count, err := decoder.VarInt()
		if err != nil || count < 0 || count > 64 {
			return false, fmt.Errorf("invalid known-pack count %d", count)
		}
		accepted := false
		for range count {
			namespace, err := decoder.String(128)
			if err != nil {
				return false, err
			}
			id, err := decoder.String(128)
			if err != nil {
				return false, err
			}
			version, err := decoder.String(128)
			if err != nil {
				return false, err
			}
			if namespace == vanillaPackNamespace && id == vanillaPackID && version == vanillaPackVersion {
				accepted = true
			}
		}
		if decoder.Remaining() != 0 {
			return false, fmt.Errorf("known-packs response has %d trailing bytes", decoder.Remaining())
		}
		return accepted, nil
	}
	return false, errors.New("client did not answer known-packs selection")
}

func waitConfigurationFinish(connection net.Conn, codec protocol.FrameCodec) error {
	for range maxConfigurationPackets {
		id, payload, err := codec.Read(connection)
		if err != nil {
			return err
		}
		if id == protocol.ConfigServerboundFinish {
			if len(payload) != 0 {
				return errors.New("finish-configuration has trailing data")
			}
			return nil
		}
	}
	return errors.New("client did not acknowledge configuration completion")
}

func emptyRequiredTags() []byte {
	registries := []string{"minecraft:block", "minecraft:item", "minecraft:fluid", "minecraft:entity_type", "minecraft:game_event"}
	var encoded protocol.Encoder
	encoded.VarInt(int32(len(registries)))
	for _, id := range registries {
		encoded.String(id)
		encoded.VarInt(0)
	}
	return encoded.Bytes()
}

func (s *Server) sendInitialPlay(connection net.Conn, codec protocol.FrameCodec, self game.Player, existing []game.Player) error {
	var login protocol.Encoder
	login.Int32(self.EntityID)
	login.Bool(false)
	login.VarInt(1)
	login.String("minecraft:overworld")
	login.VarInt(int32(s.cfg.Server.MaxPlayers))
	login.VarInt(int32(s.cfg.Server.ViewDistance))
	login.VarInt(int32(s.cfg.Server.ViewDistance))
	login.Bool(false)
	login.Bool(true)
	login.Bool(false)
	login.VarInt(0)
	login.String("minecraft:overworld")
	login.Int64(0)
	login.WriteByte(1)
	login.WriteByte(0xff)
	login.Bool(false)
	login.Bool(false)
	login.Bool(false)
	login.VarInt(0)
	login.Bool(false)
	if err := codec.Write(connection, protocol.PlayClientboundLogin, login.Bytes()); err != nil {
		return err
	}
	var abilities protocol.Encoder
	abilities.WriteByte(0x0d)
	abilities.Float32(0.05)
	abilities.Float32(0.1)
	if err := codec.Write(connection, protocol.PlayClientboundAbilities, abilities.Bytes()); err != nil {
		return err
	}
	if err := codec.Write(connection, protocol.PlayClientboundHeldItem, []byte{byte(self.SelectedHotbar)}); err != nil {
		return err
	}
	inventory, err := inventoryContentPayload(self.Inventory)
	if err != nil {
		return err
	}
	if err := codec.Write(connection, protocol.PlayClientboundInventoryContent, inventory); err != nil {
		return err
	}
	var distance protocol.Encoder
	distance.VarInt(int32(s.cfg.Server.ViewDistance))
	if err := codec.Write(connection, protocol.PlayClientboundViewDistance, distance.Bytes()); err != nil {
		return err
	}
	var spawn protocol.Encoder
	spawn.Position(s.spawn.X, s.spawn.Y, s.spawn.Z)
	spawn.Float32(0)
	if err := codec.Write(connection, protocol.PlayClientboundSpawnPosition, spawn.Bytes()); err != nil {
		return err
	}
	var position protocol.Encoder
	position.Float64(self.Position.X)
	position.Float64(self.Position.Y)
	position.Float64(self.Position.Z)
	position.Float32(self.Rotation.Yaw)
	position.Float32(self.Rotation.Pitch)
	position.WriteByte(0)
	position.VarInt(1)
	if err := codec.Write(connection, protocol.PlayClientboundPosition, position.Bytes()); err != nil {
		return err
	}
	var waiting protocol.Encoder
	waiting.WriteByte(13)
	waiting.Float32(0)
	if err := codec.Write(connection, protocol.PlayClientboundGameEvent, waiting.Bytes()); err != nil {
		return err
	}
	all := append(append(make([]game.Player, 0, len(existing)+1), existing...), self)
	if err := codec.Write(connection, protocol.PlayClientboundPlayerInfoUpdate, playerInfoPayload(all)); err != nil {
		return err
	}
	for _, other := range existing {
		if err := codec.Write(connection, protocol.PlayClientboundSpawnEntity, spawnPlayerPayload(other)); err != nil {
			return err
		}
	}
	message, err := systemChatPayload(self.Username + " joined the game")
	if err != nil {
		return err
	}
	return codec.Write(connection, protocol.PlayClientboundSystemChat, message)
}

func inventoryContentPayload(inventory []game.ItemStack) ([]byte, error) {
	items := make([]*game.ItemStack, 46)
	for index := range inventory {
		item := inventory[index]
		networkSlot, ok := inventoryNetworkSlot(item.Slot)
		if ok {
			copy := item
			items[networkSlot] = &copy
		}
	}
	var encoded protocol.Encoder
	encoded.WriteByte(0)
	encoded.VarInt(0)
	encoded.VarInt(int32(len(items)))
	for _, item := range items {
		if err := encodeItemSlot(&encoded, item); err != nil {
			return nil, err
		}
	}
	if err := encodeItemSlot(&encoded, nil); err != nil {
		return nil, err
	}
	return encoded.Bytes(), nil
}

func inventoryNetworkSlot(slot int8) (int, bool) {
	switch {
	case slot >= 0 && slot <= 8:
		return int(slot) + 36, true
	case slot >= 9 && slot <= 35:
		return int(slot), true
	case slot >= 100 && slot <= 103:
		return 108 - int(slot), true
	case slot == -106:
		return 45, true
	default:
		return 0, false
	}
}

func encodeItemSlot(encoded *protocol.Encoder, item *game.ItemStack) error {
	if item == nil || item.Count == 0 {
		encoded.VarInt(0)
		return nil
	}
	definition, exists, err := registry.ItemByName(item.ID)
	if err != nil {
		return err
	}
	if !exists || item.Count < 0 || item.Count > 99 {
		return fmt.Errorf("invalid persisted inventory item %q", item.ID)
	}
	encoded.VarInt(item.Count)
	encoded.VarInt(definition.ID)
	encoded.VarInt(0)
	encoded.VarInt(0)
	return nil
}

func newPlayerSession(parent context.Context, server *Server, identity auth.Identity, self game.Player, connection net.Conn, codec protocol.FrameCodec, events chan game.Event) *playerSession {
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	return &playerSession{server: server, identity: identity, playerID: self.ID, entityID: self.EntityID, conn: connection, codec: codec, events: events, out: make(chan outboundPacket, outboundCapacity), ctx: ctx, cancel: cancel}
}

func (p *playerSession) startWriter() {
	go func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case packet := <-p.out:
				_ = p.conn.SetWriteDeadline(time.Now().Add(p.server.cfg.Auth.LoginTimeout.Duration))
				err := p.codec.Write(p.conn, packet.id, packet.payload)
				if packet.done != nil {
					packet.done <- err
				}
				if err != nil {
					p.server.log.Debug("player packet write failed", "username", p.identity.Username, "error", err)
					_ = p.conn.Close()
					p.cancel()
					return
				}
				_ = p.conn.SetWriteDeadline(time.Time{})
			}
		}
	}()
}

func (p *playerSession) startEventWriter() {
	go func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case event, open := <-p.events:
				if !open {
					_ = p.conn.Close()
					p.cancel()
					return
				}
				if err := p.sendEvent(event); err != nil {
					_ = p.conn.Close()
					p.cancel()
					return
				}
			}
		}
	}()
}

func (p *playerSession) sendEvent(event game.Event) error {
	switch value := event.(type) {
	case game.PlayerJoined:
		if err := p.send(protocol.PlayClientboundPlayerInfoUpdate, playerInfoPayload([]game.Player{value.Player})); err != nil {
			return err
		}
		if err := p.send(protocol.PlayClientboundSpawnEntity, spawnPlayerPayload(value.Player)); err != nil {
			return err
		}
		return p.sendSystem(value.Player.Username + " joined the game")
	case game.PlayerLeft:
		var info protocol.Encoder
		info.VarInt(1)
		info.UUID([16]byte(value.Player.ID))
		if err := p.send(protocol.PlayClientboundPlayerInfoRemove, info.Bytes()); err != nil {
			return err
		}
		var entity protocol.Encoder
		entity.VarInt(1)
		entity.VarInt(value.Player.EntityID)
		if err := p.send(protocol.PlayClientboundRemoveEntities, entity.Bytes()); err != nil {
			return err
		}
		return p.sendSystem(value.Player.Username + " left the game")
	case game.PlayerMoved:
		return p.sendMovement(value)
	case game.ChatBroadcast:
		return p.sendSystem("<" + value.Sender.Username + "> " + value.Message)
	case game.BlockChanged:
		payload, err := blockUpdatePayload(value.Position, value.State)
		if err != nil {
			return err
		}
		return p.send(protocol.PlayClientboundBlockUpdate, payload)
	case game.Notice:
		return p.sendSystem(value.Message)
	case game.ViewCenterChanged:
		var center protocol.Encoder
		center.VarInt(value.Center.X)
		center.VarInt(value.Center.Z)
		return p.send(protocol.PlayClientboundViewPosition, center.Bytes())
	case game.ChunkLoaded:
		payload, err := encodeWorldChunk(value.Chunk)
		if err != nil {
			p.server.log.Warn("chunk encoding failed; sending void", "chunk_x", value.Position.X, "chunk_z", value.Position.Z, "error", err)
			payload = voidChunk(value.Position.X, value.Position.Z)
		}
		return p.sendChunk(value.Position, payload)
	case game.ChunkUnavailable:
		return p.sendChunk(value.Position, voidChunk(value.Position.X, value.Position.Z))
	case game.ChunkUnloaded:
		var unload protocol.Encoder
		unload.Int32(value.Position.Z)
		unload.Int32(value.Position.X)
		return p.send(protocol.PlayClientboundUnloadChunk, unload.Bytes())
	default:
		return fmt.Errorf("unknown game event %T", event)
	}
}

func (p *playerSession) sendChunk(_ game.ChunkPos, payload []byte) error {
	if err := p.sendChunkPacket(protocol.PlayClientboundChunkBatchStart, nil); err != nil {
		return err
	}
	if err := p.sendChunkPacket(protocol.PlayClientboundChunkData, payload); err != nil {
		return err
	}
	var finished protocol.Encoder
	finished.VarInt(1)
	return p.sendChunkPacket(protocol.PlayClientboundChunkBatchFinished, finished.Bytes())
}

func (p *playerSession) sendSystem(message string) error {
	payload, err := systemChatPayload(message)
	if err != nil {
		return err
	}
	return p.send(protocol.PlayClientboundSystemChat, payload)
}

func (p *playerSession) sendSync(id int32, payload []byte) error {
	done := make(chan error, 1)
	packet := outboundPacket{id: id, payload: append([]byte(nil), payload...), done: done}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.ctx.Done():
		return net.ErrClosed
	case p.out <- packet:
	case <-timer.C:
		_ = p.conn.Close()
		return ErrSlowClient
	}
	select {
	case err := <-done:
		return err
	case <-p.ctx.Done():
		return net.ErrClosed
	case <-timer.C:
		_ = p.conn.Close()
		return ErrSlowClient
	}
}

func (p *playerSession) send(id int32, payload []byte) error {
	packet := outboundPacket{id: id, payload: append([]byte(nil), payload...)}
	select {
	case <-p.ctx.Done():
		return net.ErrClosed
	case p.out <- packet:
		return nil
	default:
		_ = p.conn.Close()
		return ErrSlowClient
	}
}

func (p *playerSession) sendChunkPacket(id int32, payload []byte) error {
	packet := outboundPacket{id: id, payload: append([]byte(nil), payload...)}
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case <-p.ctx.Done():
		return net.ErrClosed
	case p.out <- packet:
		return nil
	case <-timer.C:
		_ = p.conn.Close()
		return ErrSlowClient
	}
}

func (s *Server) playLoop(ctx context.Context, player *playerSession) error {
	nextKeepAlive := time.Now().Add(10 * time.Second)
	lastActivity := time.Now()
	var pendingKeepAlive int64
	var pendingClear []creativeSlotChange
	for {
		if err := ctx.Err(); err != nil {
			if payload, encodeErr := textComponent("Server shutting down"); encodeErr == nil {
				_ = player.sendSync(protocol.PlayClientboundDisconnect, payload)
			}
			return err
		}
		deadline := nextKeepAlive
		idleDeadline := lastActivity.Add(s.cfg.Network.IdleTimeout.Duration)
		if idleDeadline.Before(deadline) {
			deadline = idleDeadline
		}
		_ = player.conn.SetReadDeadline(deadline)
		id, payload, err := player.codec.Read(player.conn)
		if err != nil {
			if flushErr := s.applyCreativeChanges(ctx, player, pendingClear); flushErr != nil {
				return flushErr
			}
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				now := time.Now()
				if !now.Before(idleDeadline) {
					if message, encodeErr := textComponent("Timed out"); encodeErr == nil {
						_ = player.sendSync(protocol.PlayClientboundDisconnect, message)
					}
					return errors.New("player idle timeout")
				}
				if !now.Before(nextKeepAlive) {
					pendingKeepAlive = now.UnixNano()
					var keepalive protocol.Encoder
					keepalive.Int64(pendingKeepAlive)
					if err := player.send(protocol.PlayClientboundKeepAlive, keepalive.Bytes()); err != nil {
						return err
					}
					nextKeepAlive = now.Add(10 * time.Second)
					continue
				}
			}
			return err
		}
		lastActivity = time.Now()
		if id != protocol.PlayServerboundCreativeSlot && len(pendingClear) != 0 {
			if err := s.applyCreativeChanges(ctx, player, pendingClear); err != nil {
				return err
			}
			pendingClear = nil
		}
		switch id {
		case protocol.PlayServerboundKeepAlive:
			decoder := protocol.NewDecoder(payload)
			value, err := decoder.Int64()
			if err != nil || decoder.Remaining() != 0 || pendingKeepAlive != 0 && value != pendingKeepAlive {
				return errors.New("invalid keepalive response")
			}
			pendingKeepAlive = 0
		case protocol.PlayServerboundTeleportConfirm:
			decoder := protocol.NewDecoder(payload)
			teleportID, err := decoder.VarInt()
			if err != nil || teleportID != 1 || decoder.Remaining() != 0 {
				return errors.New("invalid initial teleport acknowledgement")
			}
		case protocol.PlayServerboundChunkBatch:
			decoder := protocol.NewDecoder(payload)
			chunksPerTick, err := decoder.Float32()
			if err != nil || math.IsNaN(float64(chunksPerTick)) || math.IsInf(float64(chunksPerTick), 0) || chunksPerTick < 0 || decoder.Remaining() != 0 {
				return errors.New("invalid chunk-batch acknowledgement")
			}
		case protocol.PlayServerboundPosition:
			position, _, onGround, err := decodeMovement(payload, false)
			if err != nil {
				return err
			}
			if err := s.game.Move(ctx, game.MovePlayer{PlayerID: player.playerID, Position: position, OnGround: onGround, Moved: true}); err != nil {
				return err
			}
		case protocol.PlayServerboundPositionLook:
			position, rotation, onGround, err := decodeMovement(payload, true)
			if err != nil {
				return err
			}
			if err := s.game.Move(ctx, game.MovePlayer{PlayerID: player.playerID, Position: position, Rotation: rotation, OnGround: onGround, Moved: true, Rotated: true}); err != nil {
				return err
			}
		case protocol.PlayServerboundLook:
			_, rotation, onGround, err := decodeMovement(payload, true)
			if err != nil {
				return err
			}
			if err := s.game.Move(ctx, game.MovePlayer{PlayerID: player.playerID, Rotation: rotation, OnGround: onGround, Rotated: true}); err != nil {
				return err
			}
		case protocol.PlayServerboundFlying:
			decoder := protocol.NewDecoder(payload)
			onGround, err := decoder.Bool()
			if err != nil || decoder.Remaining() != 0 {
				return errors.New("invalid ground-state packet")
			}
			if err := s.game.Move(ctx, game.MovePlayer{PlayerID: player.playerID, OnGround: onGround}); err != nil {
				return err
			}
		case protocol.PlayServerboundPlayerAction:
			action, position, sequence, err := decodePlayerAction(payload)
			if err != nil {
				return err
			}
			if action == 0 {
				result, err := s.game.BreakBlock(ctx, player.playerID, position)
				if err != nil {
					return err
				}
				if err := s.finishBlockInteraction(player, result, sequence); err != nil {
					return err
				}
			} else if action == 1 || action == 2 {
				if err := player.sendSync(protocol.PlayClientboundBlockChangedAck, blockChangedAckPayload(sequence)); err != nil {
					return err
				}
			}
		case protocol.PlayServerboundHeldItem:
			decoder := protocol.NewDecoder(payload)
			slot, err := decoder.Int16()
			if err != nil || decoder.Remaining() != 0 {
				return errors.New("invalid held-item selection")
			}
			if err := s.game.SelectHotbar(ctx, player.playerID, int32(slot)); err != nil {
				return err
			}
		case protocol.PlayServerboundCreativeSlot:
			change, err := decodeCreativeSlot(payload)
			if err != nil {
				return err
			}
			if len(pendingClear) != 0 {
				expectedSlot := int16(len(pendingClear))
				if change.empty && change.networkSlot == expectedSlot {
					pendingClear = append(pendingClear, change)
					if change.networkSlot == 45 {
						inventory, err := s.game.ClearCreativeInventory(ctx, player.playerID)
						if err != nil {
							return err
						}
						if err := sendInventoryContent(player, inventory); err != nil {
							return err
						}
						pendingClear = nil
					}
					continue
				}
				if err := s.applyCreativeChanges(ctx, player, pendingClear); err != nil {
					return err
				}
				pendingClear = nil
			}
			if change.empty && change.networkSlot == 0 {
				pendingClear = append(pendingClear, change)
				continue
			}
			if err := s.applyCreativeChanges(ctx, player, []creativeSlotChange{change}); err != nil {
				return err
			}
		case protocol.PlayServerboundUseItemOn:
			position, sequence, mainHand, err := decodeUseItemOn(payload)
			if err != nil {
				return err
			}
			if !mainHand {
				if err := player.sendSync(protocol.PlayClientboundBlockChangedAck, blockChangedAckPayload(sequence)); err != nil {
					return err
				}
				continue
			}
			result, err := s.game.PlaceBlock(ctx, player.playerID, position)
			if err != nil {
				return err
			}
			if err := s.finishBlockInteraction(player, result, sequence); err != nil {
				return err
			}
		case protocol.PlayServerboundChatMessage:
			message, err := decodeChatMessage(payload, s.cfg.Network.MaxChatLength)
			if err != nil {
				return err
			}
			if message != "" {
				if err := s.game.Chat(ctx, game.SendChat{PlayerID: player.playerID, Message: message}); err != nil {
					return err
				}
			}
		case protocol.PlayServerboundChatCommand, protocol.PlayServerboundSignedCommand:
			command, err := decodeCommandPacket(payload, id == protocol.PlayServerboundSignedCommand, s.cfg.Network.MaxChatLength)
			if err != nil {
				return fmt.Errorf("invalid command packet: %w", err)
			}
			if err := s.game.Chat(ctx, game.SendChat{PlayerID: player.playerID, Message: "/" + command}); err != nil {
				return err
			}
		}
	}
}

func decodePlayerAction(payload []byte) (int32, game.BlockPos, int32, error) {
	decoder := protocol.NewDecoder(payload)
	action, err := decoder.VarInt()
	if err != nil || action < 0 || action > 6 {
		return 0, game.BlockPos{}, 0, errors.New("invalid player action")
	}
	x, y, z, err := decoder.Position()
	if err != nil {
		return 0, game.BlockPos{}, 0, err
	}
	face, err := decoder.Byte()
	if err != nil || face > 5 {
		return 0, game.BlockPos{}, 0, errors.New("invalid block-action face")
	}
	sequence, err := decoder.VarInt()
	if err != nil || sequence < 0 || decoder.Remaining() != 0 {
		return 0, game.BlockPos{}, 0, errors.New("invalid block-action sequence")
	}
	return action, game.BlockPos{X: x, Y: y, Z: z}, sequence, nil
}

func decodeUseItemOn(payload []byte) (game.BlockPos, int32, bool, error) {
	decoder := protocol.NewDecoder(payload)
	hand, err := decoder.VarInt()
	if err != nil || hand < 0 || hand > 1 {
		return game.BlockPos{}, 0, false, errors.New("invalid interaction hand")
	}
	x, y, z, err := decoder.Position()
	if err != nil {
		return game.BlockPos{}, 0, false, err
	}
	face, err := decoder.VarInt()
	if err != nil || face < 0 || face > 5 {
		return game.BlockPos{}, 0, false, errors.New("invalid placement face")
	}
	for range 3 {
		cursor, err := decoder.Float32()
		if err != nil || math.IsNaN(float64(cursor)) || math.IsInf(float64(cursor), 0) || cursor < 0 || cursor > 1 {
			return game.BlockPos{}, 0, false, errors.New("invalid placement cursor")
		}
	}
	if _, err := decoder.Bool(); err != nil {
		return game.BlockPos{}, 0, false, err
	}
	sequence, err := decoder.VarInt()
	if err != nil || sequence < 0 || decoder.Remaining() != 0 {
		return game.BlockPos{}, 0, false, errors.New("invalid placement sequence")
	}
	offsets := [...]game.BlockPos{{Y: -1}, {Y: 1}, {Z: -1}, {Z: 1}, {X: -1}, {X: 1}}
	offset := offsets[face]
	return game.BlockPos{X: x + offset.X, Y: y + offset.Y, Z: z + offset.Z}, sequence, hand == 0, nil
}

type creativeSlotChange struct {
	networkSlot int16
	slot        int8
	item        game.ItemStack
	apply       bool
	resync      bool
	empty       bool
}

func decodeCreativeSlot(payload []byte) (creativeSlotChange, error) {
	decoder := protocol.NewDecoder(payload)
	networkSlot, err := decoder.Int16()
	if err != nil {
		return creativeSlotChange{}, err
	}
	slot, apply, err := playerInventorySlot(networkSlot)
	if err != nil {
		return creativeSlotChange{}, err
	}
	count, err := decoder.VarInt()
	if err != nil || count < 0 || count > 99 {
		return creativeSlotChange{}, errors.New("invalid creative item count")
	}
	if count == 0 {
		if decoder.Remaining() != 0 {
			return creativeSlotChange{}, errors.New("empty creative slot has trailing data")
		}
		return creativeSlotChange{networkSlot: networkSlot, slot: slot, item: game.ItemStack{Slot: slot}, apply: apply, resync: networkSlot == 45, empty: true}, nil
	}
	if networkSlot >= 0 && networkSlot <= 4 {
		return creativeSlotChange{}, errors.New("creative crafting slots can only be cleared")
	}
	itemID, err := decoder.VarInt()
	if err != nil || itemID < 0 {
		return creativeSlotChange{}, errors.New("invalid creative item ID")
	}
	added, err := decoder.VarInt()
	if err != nil || added < 0 || added > 128 {
		return creativeSlotChange{}, errors.New("invalid added-component count")
	}
	removed, err := decoder.VarInt()
	if err != nil || removed < 0 || removed > 128 {
		return creativeSlotChange{}, errors.New("invalid removed-component count")
	}
	if added != 0 || removed != 0 || decoder.Remaining() != 0 {
		return creativeSlotChange{}, errors.New("creative item components are not supported")
	}
	item, exists, err := registry.ItemByID(itemID)
	if err != nil || !exists || count > item.StackSize {
		return creativeSlotChange{}, errors.New("unknown or oversized creative item")
	}
	return creativeSlotChange{networkSlot: networkSlot, slot: slot, item: game.ItemStack{Slot: slot, ID: "minecraft:" + item.Name, Count: count}, apply: apply}, nil
}

func (s *Server) applyCreativeChanges(ctx context.Context, player *playerSession, changes []creativeSlotChange) error {
	for _, change := range changes {
		if !change.apply {
			continue
		}
		inventory, err := s.game.SetCreativeInventorySlot(ctx, player.playerID, change.slot, change.item)
		if err != nil {
			return err
		}
		if change.resync {
			return sendInventoryContent(player, inventory)
		}
	}
	return nil
}

func sendInventoryContent(player *playerSession, inventory []game.ItemStack) error {
	payload, err := inventoryContentPayload(inventory)
	if err != nil {
		return err
	}
	return player.sendSync(protocol.PlayClientboundInventoryContent, payload)
}

func playerInventorySlot(networkSlot int16) (int8, bool, error) {
	switch {
	case networkSlot == -1:
		return 0, false, nil
	case networkSlot >= 0 && networkSlot <= 4:
		return 0, false, nil
	case networkSlot >= 9 && networkSlot <= 35:
		return int8(networkSlot), true, nil
	case networkSlot >= 36 && networkSlot <= 44:
		return int8(networkSlot - 36), true, nil
	case networkSlot >= 5 && networkSlot <= 8:
		return int8(108 - networkSlot), true, nil
	case networkSlot == 45:
		return -106, true, nil
	default:
		return 0, false, fmt.Errorf("creative inventory slot %d is unsupported", networkSlot)
	}
}

func (s *Server) finishBlockInteraction(player *playerSession, result game.BlockEditResult, sequence int32) error {
	if result.State.Name != "" {
		payload, err := blockUpdatePayload(result.Position, result.State)
		if err != nil {
			return err
		}
		if err := player.sendSync(protocol.PlayClientboundBlockUpdate, payload); err != nil {
			return err
		}
	}
	if result.Err != nil {
		s.log.Warn("block interaction rejected", "username", player.identity.Username, "x", result.Position.X, "y", result.Position.Y, "z", result.Position.Z, "error", result.Err)
	}
	return player.sendSync(protocol.PlayClientboundBlockChangedAck, blockChangedAckPayload(sequence))
}

func blockChangedAckPayload(sequence int32) []byte {
	var encoded protocol.Encoder
	encoded.VarInt(sequence)
	return encoded.Bytes()
}

func blockUpdatePayload(position game.BlockPos, state world.BlockState) ([]byte, error) {
	id, err := registry.BlockStateID(state.Name, state.Properties)
	if err != nil {
		return nil, err
	}
	var encoded protocol.Encoder
	encoded.Position(position.X, position.Y, position.Z)
	encoded.VarInt(id)
	return encoded.Bytes(), nil
}

func decodeChatMessage(payload []byte, maxLength int) (string, error) {
	decoder := protocol.NewDecoder(payload)
	message, err := decoder.String(maxLength)
	if err != nil {
		return "", err
	}
	if _, err := decoder.Int64(); err != nil {
		return "", fmt.Errorf("read chat timestamp: %w", err)
	}
	if _, err := decoder.Int64(); err != nil {
		return "", fmt.Errorf("read chat salt: %w", err)
	}
	hasSignature, err := decoder.Bool()
	if err != nil {
		return "", err
	}
	if hasSignature {
		if _, err := decoder.Bytes(256); err != nil {
			return "", err
		}
	}
	if _, err := decoder.VarInt(); err != nil {
		return "", err
	}
	if _, err := decoder.Bytes(3); err != nil {
		return "", err
	}
	if decoder.Remaining() != 0 {
		return "", fmt.Errorf("chat packet has %d trailing bytes", decoder.Remaining())
	}
	return message, nil
}

func decodeMovement(payload []byte, rotation bool) (game.Vec3, game.Rotation, bool, error) {
	decoder := protocol.NewDecoder(payload)
	var position game.Vec3
	var look game.Rotation
	var err error
	if len(payload) == 9 && rotation {
		look.Yaw, err = decoder.Float32()
		if err == nil {
			look.Pitch, err = decoder.Float32()
		}
	} else {
		position.X, err = decoder.Float64()
		if err == nil {
			position.Y, err = decoder.Float64()
		}
		if err == nil {
			position.Z, err = decoder.Float64()
		}
		if err == nil && rotation {
			look.Yaw, err = decoder.Float32()
		}
		if err == nil && rotation {
			look.Pitch, err = decoder.Float32()
		}
	}
	if err != nil {
		return game.Vec3{}, game.Rotation{}, false, err
	}
	onGround, err := decoder.Bool()
	if err != nil || decoder.Remaining() != 0 {
		return game.Vec3{}, game.Rotation{}, false, errors.New("invalid movement packet")
	}
	return position, look, onGround, nil
}

func playerInfoPayload(players []game.Player) []byte {
	var encoded protocol.Encoder
	encoded.WriteByte(0x1d)
	encoded.VarInt(int32(len(players)))
	for _, player := range players {
		encoded.UUID([16]byte(player.ID))
		encoded.String(player.Username)
		encoded.VarInt(int32(len(player.Properties)))
		for _, property := range player.Properties {
			encoded.String(property.Name)
			encoded.String(property.Value)
			encoded.Bool(property.Signature != "")
			if property.Signature != "" {
				encoded.String(property.Signature)
			}
		}
		encoded.VarInt(1)
		encoded.Bool(true)
		encoded.VarInt(0)
	}
	return encoded.Bytes()
}

func spawnPlayerPayload(player game.Player) []byte {
	var encoded protocol.Encoder
	encoded.VarInt(player.EntityID)
	encoded.UUID([16]byte(player.ID))
	encoded.VarInt(registry.PlayerEntityTypeID)
	encoded.Float64(player.Position.X)
	encoded.Float64(player.Position.Y)
	encoded.Float64(player.Position.Z)
	encoded.WriteByte(angleByte(player.Rotation.Pitch))
	encoded.WriteByte(angleByte(player.Rotation.Yaw))
	encoded.WriteByte(angleByte(player.Rotation.Yaw))
	encoded.VarInt(0)
	encoded.Int16(0)
	encoded.Int16(0)
	encoded.Int16(0)
	return encoded.Bytes()
}

func (p *playerSession) sendMovement(event game.PlayerMoved) error {
	var packetID int32
	var encoded protocol.Encoder
	dx := int(math.Round((event.Player.Position.X - event.Previous.Position.X) * 4096))
	dy := int(math.Round((event.Player.Position.Y - event.Previous.Position.Y) * 4096))
	dz := int(math.Round((event.Player.Position.Z - event.Previous.Position.Z) * 4096))
	relative := dx >= math.MinInt16 && dx <= math.MaxInt16 && dy >= math.MinInt16 && dy <= math.MaxInt16 && dz >= math.MinInt16 && dz <= math.MaxInt16
	if event.Moved && !relative {
		packetID = protocol.PlayClientboundEntityTeleport
		encoded.VarInt(event.Player.EntityID)
		encoded.Float64(event.Player.Position.X)
		encoded.Float64(event.Player.Position.Y)
		encoded.Float64(event.Player.Position.Z)
		encoded.WriteByte(angleByte(event.Player.Rotation.Yaw))
		encoded.WriteByte(angleByte(event.Player.Rotation.Pitch))
		encoded.Bool(event.Player.OnGround)
	} else if event.Moved && event.Rotated {
		packetID = protocol.PlayClientboundMoveLook
		encoded.VarInt(event.Player.EntityID)
		encoded.Int16(int16(dx))
		encoded.Int16(int16(dy))
		encoded.Int16(int16(dz))
		encoded.WriteByte(angleByte(event.Player.Rotation.Yaw))
		encoded.WriteByte(angleByte(event.Player.Rotation.Pitch))
		encoded.Bool(event.Player.OnGround)
	} else if event.Moved {
		packetID = protocol.PlayClientboundRelativeMove
		encoded.VarInt(event.Player.EntityID)
		encoded.Int16(int16(dx))
		encoded.Int16(int16(dy))
		encoded.Int16(int16(dz))
		encoded.Bool(event.Player.OnGround)
	} else if event.Rotated {
		packetID = protocol.PlayClientboundEntityLook
		encoded.VarInt(event.Player.EntityID)
		encoded.WriteByte(angleByte(event.Player.Rotation.Yaw))
		encoded.WriteByte(angleByte(event.Player.Rotation.Pitch))
		encoded.Bool(event.Player.OnGround)
	} else {
		return nil
	}
	if err := p.send(packetID, encoded.Bytes()); err != nil {
		return err
	}
	if event.Rotated {
		var head protocol.Encoder
		head.VarInt(event.Player.EntityID)
		head.WriteByte(angleByte(event.Player.Rotation.Yaw))
		return p.send(protocol.PlayClientboundHeadRotation, head.Bytes())
	}
	return nil
}

func angleByte(angle float32) byte { return byte(int8(math.Floor(float64(angle * 256 / 360)))) }

func systemChatPayload(text string) ([]byte, error) {
	component, err := textComponent(text)
	if err != nil {
		return nil, err
	}
	return append(component, 0), nil
}

func playDisconnect(connection net.Conn, codec protocol.FrameCodec, reason string) error {
	component, err := textComponent(reason)
	if err != nil {
		return err
	}
	return codec.Write(connection, protocol.PlayClientboundDisconnect, component)
}

func configurationDisconnect(connection net.Conn, codec protocol.FrameCodec, reason string) error {
	component, err := textComponent(reason)
	if err != nil {
		return err
	}
	return codec.Write(connection, protocol.ConfigClientboundDisconnect, component)
}

func textComponent(text string) ([]byte, error) {
	return protocol.EncodeNetworkNBT(map[string]any{"text": text})
}
