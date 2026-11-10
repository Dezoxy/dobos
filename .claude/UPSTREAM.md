# Harness provenance

Most agents, skills, rules and commands under `.claude/`, and the Node hook
runtime in `.claude/hooks/node/` and `.claude/hooks/lib/`, come from ECC
(`affaan-m/ECC`, MIT; the licence is [UPSTREAM-LICENSE](UPSTREAM-LICENSE)).
They were copied from the fork `Dezoxy/ECC`, which is checked out beside this
repository as `agent-base` and is never edited by hand: it only follows
upstream.

- Upstream pin: `affaan-m/ECC@c70874fa`. The fork matches it exactly;
  `rules/common/merge-discipline.md` is the owner's own rule and lives only
  here.
- Copied on: 2026-09-30

## Byte-identical to upstream

- `.claude/hooks/node/` and `.claude/hooks/lib/` (upstream `scripts/hooks/`
  and `scripts/lib/`): gateguard, session start and end, pre-compact and the
  libraries they load, 22 files. Never edit them; tune them through the
  `env` block in `.claude/settings.json` (`GATEGUARD_*`, `ECC_HOOK_PROFILE`,
  `ECC_DISABLED_HOOKS`).
- Every copied skill, rule and command not named below.

## Taken from meridian-ai-platform

The owner's tuned copies win over raw ECC:

- Agents `python-reviewer`, `security-reviewer`, `silent-failure-hunter`,
  `database-reviewer`, `fastapi-reviewer`, `rag-pipeline-reviewer` and
  `tdd-guide`: `effort: high`.
- Agents `implementer` and `infra-reviewer`: meridian's own, made
  repository- and cloud-neutral here.
- Skill `mcp-server-patterns`: a dead relative link made absolute.
- `scripts/codex_agents.py` and `.codex/` (the hook wiring, the link to
  `.claude/hooks` and the config example): meridian's own, without its
  boundary hook and its prompt file. The generator gained `--check` and
  removes the twin of a deleted agent. The prompt-defence baseline it
  prepends to every twin is ECC's text.
- Skill `feature-threat-model`: meridian's own, made project-neutral here.
  Its assets and invariants are two lists the consuming repository writes.
- Hooks `guard-bash.sh` (meridian-only Azure, kind and Makefile rules
  removed; git hook-bypass denies added here), `check-py.sh`, `check-iac.sh`
  (chart path generalised) and `check-docs.sh`.
- `tests/test_guard_bash.sh` and 85 of meridian's 114 guard-bash cases. The
  other 29 exercise the removed meridian-only rules.
- The hook-bypass rules were reworked here on 2026-10-04, from the findings
  of meridian's security review and of a second one of the rework itself:
  prose (a commit message, a pull request body) is read as prose, and a
  flag after it is still seen. 77 cases pin both directions. A consumer
  that keeps rules of its own re-copies the block, not the file.
- `settings.json`: the model, effort and advisor keys, and the `allow`,
  `ask` and `deny` lists, without meridian's Azure and kind entries.
  `ECC_SKIP_LLM_SUMMARY=1`: without it the session-end hook starts a
  `claude -p` child that runs with the repository's pre-approved tools.

## Hand edits to ECC files

- Agents `code-reviewer`, `typescript-reviewer`: `effort: high`, like the
  other reviewers. `typescript-reviewer` points at the `react-reviewer` agent
  directly; the `/react-review` command does not ship here.
- Skill `react-patterns`: its related list names what ships here; the React
  rules, slash commands and `react-build-resolver` (now `build-error-resolver`)
  do not.
- Command `skill-create`: the `--instincts` mode for ECC's continuous-learning
  system, and the pitch for a third-party GitHub App that reads the
  repository, are removed.
- Agent `build-error-resolver`, rule `development-workflow`: no `planner` or
  `architect` agent ships here, so that work is reported back instead.
- Agent `homelab-architect`, skills `homelab-network-readiness` and
  `homelab-network-setup`: the network skills they named are replaced by the
  `network-config-reviewer` and `network-troubleshooter` agents.
- Rule `agents`: roster-neutral. It points at the consuming repository's
  `.claude/agents/`, invoked by name, because a roster in a vendored rule is
  wrong for every repository but one. Upstream lists the `ecc:` plugin
  namespace, which is not installed.
- Rule `code-review`: the review table lists the reviewers that ship here,
  and says to skip a row whose agent the consuming repository lacks.
- Command `resume-session`: with no argument, or a date, it considers only
  session files recorded for the current repository, as the session-start
  hook does. Upstream takes the newest file of any project.
- `.mcp.json` at the repository root: the `chrome-devtools` server the ECC
  plugin ships, at the same pinned version, with Google's usage statistics,
  the CrUX lookups and the update check switched off.
- Rule `git-workflow`: ECC's note that its installer turns off
  `Co-Authored-By` is removed; attribution stays on.
- Rule `python/coding-style`: ruff formats and sorts imports, not black and
  isort.
- Rules `typescript/coding-style` and `typescript/hooks`: `console.log` is
  allowed as deliberate logging on Cloudflare Workers, whose logs it feeds.
- Skills `react-patterns`, `react-testing`: their links to the React rules
  are plain paths, because a relative link cannot resolve from both
  `.claude/skills/` and the `.agents/skills/` mirror.
- Skill `docker-patterns`: the section on ECC's own installer test harness is
  removed. Skill `github-ops`: ECC's maintainer release checklist and the
  paragraph pointing at it are removed. Skill `search-first`: the integration
  section naming agents that do not ship here is removed. Skill
  `postgres-patterns`: related-skill pointers trimmed to skills that ship.
  Skill `gateguard`: points at the vendored hook.

A "see also" mention of an ECC skill that does not ship here is left
verbatim: it costs a failed lookup at worst, and editing it would make every
upstream diff noisier.

## Added for Dobos

Copied from `agent-base` (ECC) at the same pin, on top of what
development-base ships: rules `rules/golang/`, skills `golang-patterns` and
`golang-testing`, agents `go-reviewer` and `go-build-resolver`. The two
agents gained `effort: high` in their frontmatter, to match the routing in
`CLAUDE.md`; nothing else was edited.

## Checking upstream drift

After `agent-base` has synced, list what upstream changed since the pin in the
areas copied here, then review the files this repository carries:

```bash
git -C ../agent-base fetch upstream
git -C ../agent-base diff --stat c70874fa upstream/main -- \
  agents skills rules commands scripts/hooks scripts/lib
```

Upstream `scripts/hooks/` lands in `.claude/hooks/node/`, `scripts/lib/` in
`.claude/hooks/lib/` and `rules/<set>/` in `.claude/rules/<set>/`; agents,
skills and commands keep their folder names under `.claude/`. Take a change by
re-copying the file and re-applying the edits listed above, then move the pin.
