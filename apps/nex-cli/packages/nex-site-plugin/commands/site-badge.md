---
description: Hide or show the Nex badge on the linked Nex Sites site.
argument-hint: "hide | show"
skills: site-deploy
---

Use the `site-deploy` skill for this request:

$ARGUMENTS

Run `nex-site badge hide` (or `show` when the user wants it back) and report `badgeVisible`. Only paid plans can hide the badge; on error 3608 tell the user the free plan always shows it and they need to upgrade their Nex plan.
