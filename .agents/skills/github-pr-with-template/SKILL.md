---
name: github-pr-with-template
description: >
  Use when the user asks to create a pull request from the current non-master
  branch with GitHub CLI, using .github/pull_request_template.md as the body
  template, and cleaning up the temporary pr-message.md file whether creation
  succeeds or fails.
version: "1.0.0"
author: "User"
license: "MIT"
---

# GitHub PR With Template

Use this skill when the user wants Codex to open a GitHub pull request with `gh`.

## Boundaries

- This workflow does **not** use the `docs/exec-plan/active/` plan trio.
- Only project-function development, code optimization, or bug fixes use the plan trio.
- Other operations, including branch switching, PR creation, comments, labels, release chores, and similar repo operations, strictly do **not** use the plan trio.

## Required Checks

Before creating the PR:

1. Confirm the current branch is not `master`.
2. Confirm the target base branch actually exists on remote.
3. Read `.github/pull_request_template.md`.
4. Summarize the real branch changes from git history and diff.
5. Fill a temporary PR body file, default path: `/tmp/pr-message.md`.

If the user gave a base branch that does not exist, report it and use the real base only after user confirmation.

## Create PR

Preferred flow:

1. Build the PR title and body from the template and actual diff.
2. Use `scripts/create_pr.sh "<title>" "<base-branch>"`.
3. If it succeeds, report the PR URL.
4. If it fails, show the generated PR body to the user so they can submit manually.

## Cleanup Rule

- Always delete the temporary `pr-message.md` file after `gh pr create`.
- Success path: delete it and return the PR URL.
- Failure path: print the body content for the user, delete it, then return failure.

## Suggested Inputs

- PR title
- Base branch
- Optional body file path if the caller does not want `/tmp/pr-message.md`

## Verification

After running the script:

- On success, verify the returned output is a GitHub PR URL.
- On failure, verify the temporary body file has been removed before responding.
