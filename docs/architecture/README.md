# Architecture

## System

Flodni

## Purpose

Presents torrents as ordinary media files and streams their bytes on demand
to a media server.

## Architecture model

- [workspace.dsl](workspace.dsl) — entry point (fragments in [model/](model/))
- `make check` validates the model with the pinned Structurizr image;
  `make view` serves it at <http://localhost:8080/workspace/1>.

## Important views

| View | Question it answers |
|---|---|
| SystemContext | What does Flodni depend on, and who depends on it? |
| Containers | What runs, and which stores does it own? |
| Daemon | What are the parts of the single process? |

## Key decisions

- [0001 Hard-fork Tiramisu as Flodni](decisions/0001-hard-fork-tiramisu.md)

New ADR: copy [templates/adr.md](templates/adr.md) to
`decisions/NNNN-short-title.md`. Keep this index up to date; there is
deliberately no README inside `decisions/`, because the ADR importer parses
every `.md` file in that folder.

## Written documentation

| Area | Documents |
|---|---|
| Narrative (opens the tab and the PDF) | [01 overview](overview/01-flodni.md) |

Only `overview/` is imported into the model by `!docs`.

## Not documented here

The plan ([roadmap](../roadmap.md)), unplanned ideas ([ideas](../ideas.md))
and the inherited user guide
([README.upstream.md](../../README.upstream.md)).
