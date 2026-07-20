// SPDX-License-Identifier: AGPL-3.0-only

package protocol

// Minecraft 1.21.1 (protocol 767) packet identifiers used by the MVP.
const (
	HandshakeServerboundIntention int32 = 0x00

	StatusServerboundRequest  int32 = 0x00
	StatusServerboundPing     int32 = 0x01
	StatusClientboundResponse int32 = 0x00
	StatusClientboundPong     int32 = 0x01

	LoginServerboundStart              int32 = 0x00
	LoginServerboundEncryptionResponse int32 = 0x01
	LoginServerboundAcknowledged       int32 = 0x03
	LoginClientboundDisconnect         int32 = 0x00
	LoginClientboundEncryptionRequest  int32 = 0x01
	LoginClientboundSuccess            int32 = 0x02

	ConfigClientboundDisconnect       int32 = 0x02
	ConfigClientboundFinish           int32 = 0x03
	ConfigClientboundRegistryData     int32 = 0x07
	ConfigClientboundFeatureFlags     int32 = 0x0c
	ConfigClientboundTags             int32 = 0x0d
	ConfigClientboundSelectKnownPacks int32 = 0x0e

	ConfigServerboundClientInformation int32 = 0x00
	ConfigServerboundFinish            int32 = 0x03
	ConfigServerboundSelectKnownPacks  int32 = 0x07

	PlayClientboundSpawnEntity        int32 = 0x01
	PlayClientboundSpawnExperienceOrb int32 = 0x02
	PlayClientboundBlockChangedAck    int32 = 0x05
	PlayClientboundBlockUpdate        int32 = 0x09
	PlayClientboundChunkBatchFinished int32 = 0x0c
	PlayClientboundChunkBatchStart    int32 = 0x0d
	PlayClientboundInventoryContent   int32 = 0x13
	PlayClientboundSetSlot            int32 = 0x15
	PlayClientboundDisconnect         int32 = 0x1d
	PlayClientboundUnloadChunk        int32 = 0x21
	PlayClientboundGameEvent          int32 = 0x22
	PlayClientboundKeepAlive          int32 = 0x26
	PlayClientboundChunkData          int32 = 0x27
	PlayClientboundLogin              int32 = 0x2b
	PlayClientboundRelativeMove       int32 = 0x2e
	PlayClientboundMoveLook           int32 = 0x2f
	PlayClientboundEntityLook         int32 = 0x30
	PlayClientboundAbilities          int32 = 0x38
	PlayClientboundPlayerInfoRemove   int32 = 0x3d
	PlayClientboundPlayerInfoUpdate   int32 = 0x3e
	PlayClientboundPosition           int32 = 0x40
	PlayClientboundRemoveEntities     int32 = 0x42
	PlayClientboundHeadRotation       int32 = 0x48
	PlayClientboundHeldItem           int32 = 0x53
	PlayClientboundViewPosition       int32 = 0x54
	PlayClientboundViewDistance       int32 = 0x55
	PlayClientboundSpawnPosition      int32 = 0x56
	PlayClientboundEntityMetadata     int32 = 0x58
	PlayClientboundEntityEquipment    int32 = 0x5b
	PlayClientboundSetPassengers      int32 = 0x5f
	PlayClientboundSystemChat         int32 = 0x6c
	PlayClientboundEntityTeleport     int32 = 0x70

	PlayServerboundTeleportConfirm int32 = 0x00
	PlayServerboundChatCommand     int32 = 0x04
	PlayServerboundSignedCommand   int32 = 0x05
	PlayServerboundChatMessage     int32 = 0x06
	PlayServerboundChunkBatch      int32 = 0x08
	PlayServerboundContainerClick  int32 = 0x0e
	PlayServerboundUseEntity       int32 = 0x16
	PlayServerboundKeepAlive       int32 = 0x18
	PlayServerboundPosition        int32 = 0x1a
	PlayServerboundPositionLook    int32 = 0x1b
	PlayServerboundLook            int32 = 0x1c
	PlayServerboundFlying          int32 = 0x1d
	PlayServerboundPlayerAction    int32 = 0x24
	PlayServerboundHeldItem        int32 = 0x2f
	PlayServerboundCreativeSlot    int32 = 0x32
	PlayServerboundUseItemOn       int32 = 0x38
)
