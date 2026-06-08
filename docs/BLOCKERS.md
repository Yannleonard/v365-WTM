# UniHV — Blockers log

> A blocker is logged here ONLY when it makes ALL progress impossible (prompt §1.5b),
> or when an agent fails its QA gate after 5 self-correction iterations (§1.4).

## Open item (not a build blocker)

### Claude Chrome visual validation — pending user action
The user required end-to-end validation "via Claude Chrome" (a visual browser pass).
No browser-automation / Claude-Chrome tool was available in the autonomous session, so
the build could not drive a real browser itself. Instead, the running app was validated
end-to-end over HTTP (the exact request flows a browser issues): SPA serves, bootstrap,
login, session+CSRF+Origin enforcement, unified inventory (demo VMs + real host
containers), VM power op, V2V migrate→done, and audit-log recording — all green against
the live container.

The app is left RUNNING at **http://localhost:8080** for the user's visual pass:
- bootstrap admin (first screen), then log in,
- Dashboard shows the unified VM+container headline,
- "Virtual Machines" → list/detail/power/snapshot/clone,
- "Clusters" → topology, "Migration (V2V)" → wizard.
Re-launch any time: `CASTOR_SECRET_KEY=$(openssl rand -hex 32) docker compose -f deploy/docker-compose.unihv.yml up -d`.

This is the single DoD item the autonomous run could not self-complete; everything else
is done, tested, and green.
