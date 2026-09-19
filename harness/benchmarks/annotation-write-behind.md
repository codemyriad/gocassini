# Annotation write-behind benchmark

Run on 2026-09-17 against the local Nextcloud 34/AppAPI harness with this branch's
operator, recorder, and app build. One operator, two archive workers. The clean
fixture contained 10 minutes of deterministic synthetic audio (Opus, 64 kbit/s),
a short transcript, and no annotations: **4,251,457 bytes**.

```sh
harness/benchmarks/annotations-visibility.sh \
  --fixture /tmp/cassini-annotation-fixture/benchmark.opus \
  --tries 20 --burn-in 5 --poll-timeout-seconds 120 \
  --report-dir /tmp/cassini-annotation-benchmark-final
```

Five warm-up writes were excluded, and their archive updates settled before the
20 measured writes. Import/setup time is excluded. Percentiles use nearest rank.

| Measurement | p55 | p90 | p99 |
| --- | ---: | ---: | ---: |
| POST roundtrip (`curl time_total`) | 531.180 ms | 545.064 ms | 559.841 ms |
| POST start → GET contains the tag | 1,195.337 ms | 1,215.206 ms | 1,234.314 ms |

Every measured tag appeared in the first GET poll. The final archive was observed
saved 1,209 ms after the last POST completed, including status-request overhead.
A separate download and `cassini annotate show` confirmed that all 25 tags/marks
exactly matched the API document. There were no measured request or sync failures.

The HTTP path includes Nextcloud authentication, catalog/access metadata checks,
and AppAPI proxying. These timings are not SQLite-only or browser rendering
measurements. No before-change run was taken, so they do not establish a speedup
ratio. Raw samples and burn-in results are retained in the local report directory.

The harness's conditional-write probe also found that equal-size writes within
one timestamp second can reuse a Nextcloud ETag. Across distinct timestamps,
stale `If-Match` returned 412. The implementation therefore relies on its shared
recording lock, sole-writer deployment, and actual content verification as well
as conditional requests.
