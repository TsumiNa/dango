---
description: 'Use when refactoring or evolving in-progress APIs within the same branch, or deciding whether to add compatibility wrappers, adapter layers, or deprecated aliases for interface changes under development.'
name: 'In-Branch API Compatibility'
---

# In-Branch API Compatibility

- For APIs that are only in progress on the current branch, add wrappers, adapter layers, deprecated aliases, or parallel interfaces only when the user asks in the current request to preserve compatibility, such as by keeping an old name, adapter, or alias.
- Prefer updating call sites directly and keeping one current interface.
- If compatibility may matter for released code, cross-branch coordination, or external consumers, prioritize that concern over the in-branch cleanup rule and state the assumption before adding compatibility code.
