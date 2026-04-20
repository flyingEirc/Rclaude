# reference

This directory stores version-pinned reference notes for third-party tools, frameworks, SDKs, APIs, and protocol specs.

## Current entries

- `pty-protocol.md`: quick reference for the Remote PTY service, frame layout, error enums, and handshake order.

## Positioning

- Record dependency versions, capability boundaries, compatibility notes, and official sources.
- Provide accurate, version-consistent external context for implementation work.
- Do not replace design docs or carry project-internal architecture decisions.

## Required content

- Third-party name and version
- Official documentation link or source note
- How this repository uses the dependency or protocol
- Important limits, pitfalls, and compatibility conclusions
- The smallest necessary example related to this repository

## Constraints

- Before implementing against a third-party library, framework, SDK, or API, add or verify the corresponding reference note.
- Reference notes must include explicit versions instead of vague wording like "latest".
- If an external change affects design or implementation, update the related design or exec-plan documents as well.
