# Storage and filesystem

Assume:

- `work-root = /var/lib/cassini-operator/jobs`
- `site-root = /srv/cassini-site/published`

## Canonical reusable artifacts

```text
/var/lib/cassini-operator/jobs/current/
  <job-id>.run/
  <job-id>.meeting/
```

These are the stable inputs for downstream reruns and full-site publish.

## Attempt-local retained artifacts

```text
/var/lib/cassini-operator/jobs/runs/
  <job-id>--attempt-001.run/
  <job-id>--attempt-001.logs/
    record.log
    build.log
    publish.log
  <job-id>--attempt-002.meeting/
  <job-id>--attempt-002.site/
```

These preserve execution history and logs.

## Live published site

```text
/srv/cassini-site/published/
```

This is what the viewer serves.

## Staging roots

The operator also uses transient staging roots during promotion, including:

- `current/.staging/` for canonical `.run` and `.meeting` promotion
- `<site-root>.staging/` for live-site replacement and rollback handling

These are transient workspaces, not retained outputs.

## What summary-row paths mean

At the logical job summary level:

- `artifact_run_path` points at canonical `current/<job-id>.run`
- `artifact_meeting_path` points at canonical `current/<job-id>.meeting`
- `artifact_site_path` points at the live shared site root

## What attempt-row paths mean

At the attempt level:

- `.run`, `.meeting`, and `.site` paths point at attempt-local retained outputs when that attempt created them
- rerun attempts typically reuse the canonical `.run` and create fresh attempt-local `.meeting` and `.site` outputs

## Volume model in deployment

Default named volumes:

- `cassini_operator_state`
- `cassini_published_site`

The operator state volume holds:

- SQLite DB
- work-root artifacts
- caches
- temp files

The published-site volume holds:

- the live `published/` site
- adjacent staging space used during promotion
