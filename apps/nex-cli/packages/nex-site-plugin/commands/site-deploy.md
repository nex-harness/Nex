---
description: Deploy this project to Nex Sites, or redeploy the linked site.
argument-hint: "[site id or notes]"
skills: site-deploy
---

Use the `site-deploy` skill for this request:

$ARGUMENTS

The user asked for Nex Sites explicitly, so the target is settled, but still confirm the upload with the user as the skill's step 1 describes before changing or uploading anything. Then follow the skill end to end: adapt and build the project into a stage directory, `nex-site check` it, then publish it with `nex-site deploy <stage-dir>`. If the arguments name a site id, pass it as `--site <id>`; otherwise a project without `.nex-site.json` deploys with `--new --name --prefix` (see the skill's step 7). Finish with the site URL.
