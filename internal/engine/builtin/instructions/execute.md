# Execute stage note

Perform the assigned task using the loaded `SKILL.md`, available tools, and runner-exposed workspace paths.

- Use tools to inspect the exchange and upstream handoff references listed in the runtime context when the task needs their contents.
- Prefer the skill's documented canonical script entrypoint when it fits the task.
- Write durable downstream files under `downstream/artifacts/` and list them in handoff front matter.
- Write durable private notes under `memo/` only when assumptions, data quality, failed attempts, or decisions should survive context loss.
- Return one Dango handoff markdown document for downstream consumers.
