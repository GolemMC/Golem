# Golem Roadmap

The live backlog is tracked in the [Golem Roadmap](https://github.com/orgs/GolemMC/projects/1) on GitHub Projects. This file outlines the major upcoming milestones for the server.

1. **Inventory & Containers**
   Finish inventory and container rules, including item components, crafting, armor validation, and ordinary inventory clicks.
2. **Block Placement & State**
   Add correct placement rules for directional, waterlogged, multipart, and block-entity-backed blocks.
3. **Survival Loop**
   Build the minimum Survival loop: health, hunger, damage, drops, tools, experience, and respawning.
4. **Entities**
   Connect loaded entities to the game loop, then add ticking, movement, spawning, and saving.
5. **Terrain & Dimensions**
   Add terrain generation and dimension support without weakening the existing world-safety checks.
6. **Commands & Plugin API**
   Expand commands and permissions before designing a stable plugin API.
7. **Testing & Alpha Release**
   Add protocol fuzzing, long-running multiplayer tests, and benchmarks before publishing an alpha release.
