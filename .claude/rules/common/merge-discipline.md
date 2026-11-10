# Merge Discipline

> Local rule, not from upstream ECC. Kept as its own file so an upstream sync
> never conflicts with it.

## Never stack pull requests

Branch every change off the default branch. Do not open a pull request whose
base is another open pull request's branch.

On a **squash-merging** repository a stacked branch can merge into a base that
has already been merged and deleted. The merge succeeds, GitHub shows it green,
and the content never reaches the default branch. This is silent: nothing fails,
and the pull request page says "Merged".

If work genuinely depends on unmerged work, wait for the parent to merge, then
branch fresh from the default branch and replay the commits.

## Verify the content landed, not the merge status

After a merge, `git fetch` and check that the change is actually there:

```bash
git show origin/main:path/to/expected/file >/dev/null && echo present
```

`git branch --merged` does not answer this on a squash-merging repository:
squashing creates a new commit, so the branch tip never becomes an ancestor of
the default branch and every branch reports as unmerged.

Comparing whole trees over-reports too — a file a *later* change touched will
differ without anything being lost. Compare the files the branch itself changed.

## Replay, do not re-target

When a branch must move to a different base, branch from the new base and
cherry-pick. Re-targeting an old branch silently carries whatever that branch
missed: if it was cut before another change landed, merging it reverts that
change, and a check that would have caught the revert may be the very thing
being reverted.

## Verify a claim before writing it into an instruction file

Anything that becomes a rule — repository visibility, a version, who owns a
value — gets checked against the source first. A conditional in a template
("a public repository must…") is not a statement about the repository in front
of you.
