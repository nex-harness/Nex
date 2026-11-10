---
description: Show the logs of a Nex Sites deployment and explain any failure.
argument-hint: "[deployment id]"
skills: site-deploy
---

Use the `site-deploy` skill for this request:

$ARGUMENTS

Find the deployment: use the id in the arguments, or the latest deployment from `nex-site status`. Read it with `nex-site logs <deploymentId>` and explain what happened. For a failure, give the cause and the fix (see the skill's troubleshooting reference).
