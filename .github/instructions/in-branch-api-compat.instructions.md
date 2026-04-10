---
description: 'Use when refactoring or evolving in-progress APIs within the same branch, or deciding whether to add compatibility wrappers, adapter layers, or deprecated aliases for interface changes under development.'
name: 'In-Branch API Compatibility'
---

# In-Branch API Compatibility

- Unless the user explicitly requests compatibility, do not add wrappers, adapter layers, deprecated aliases, or parallel interfaces solely to preserve compatibility for in-progress API changes within the same branch.
- Prefer updating call sites directly and keeping one current interface.
- If compatibility may matter for released code, cross-branch coordination, or external consumers, state that assumption explicitly before adding compatibility code.
