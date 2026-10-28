#!/bin/bash
set -euo pipefail

cd "${CLAUDE_PROJECT_DIR:-.}"

# Download Go modules in remote sessions (cache lives outside the repo, so this runs every time; it is a no-op when cached)
if [ "${CLAUDE_CODE_REMOTE:-}" = "true" ] && command -v go >/dev/null 2>&1; then
  go mod download >&2
fi

# Inject the using-superpowers skill into the session context
skill_md=".claude/skills/using-superpowers/SKILL.md"
[ -r "$skill_md" ] || exit 0
content="$(<"$skill_md")"

s="<EXTREMELY_IMPORTANT>
You have superpowers.

**Below is the full content of your 'using-superpowers' skill - your introduction to using skills. For all other skills, use the 'Skill' tool:**

${content}
</EXTREMELY_IMPORTANT>"
s="${s//\\/\\\\}"
s="${s//\"/\\\"}"
s="${s//$'\n'/\\n}"
s="${s//$'\r'/\\r}"
s="${s//$'\t'/\\t}"

printf '{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"%s"}}\n' "$s"
