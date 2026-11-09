# Domain Docs

How the engineering skills should consume this repo's domain documentation when exploring the codebase.

## Before exploring, read these

- `GLOSSARY.md` at the repo root, or `GLOSSARY-MAP.md` if it exists.
- `docs/adr/`: read ADRs that touch the area being worked on.

If these files do not exist, proceed silently. The domain-modeling skill creates them lazily when terms or decisions are resolved.

## File structure

This is a single-context repository:

```
/
├── GLOSSARY.md
├── docs/adr/
└── src/
```

## Use the glossary's vocabulary

Use terms defined in `GLOSSARY.md` in issue titles, refactor proposals, hypotheses, and tests. If the concept is not defined, reconsider the term or note the gap for domain-modeling.

## Flag ADR conflicts

Surface any conflict with an existing ADR explicitly rather than silently overriding it.
