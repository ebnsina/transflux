# Testing

Three kinds of test, and one rule: a slice is not done because the code
compiles. Every non-trivial behaviour leaves behind something that fails if the
behaviour breaks.

## Running

```sh
make test                       # unit tests only; anything needing a service skips itself
make up && make test-all        # everything, against the Compose stack
cd web && npm run lint && npm run check && npm run build
```

Integration tests skip with a message rather than failing when
`TRANSFLUX_TEST_DATABASE_URL` or `TRANSFLUX_TEST_S3_ENDPOINT` is unset, so a
fresh clone runs green without any services.

`make test-all` uses `-p 1`. The integration tests share one database and lease
from a queue that is global by design, so package tests running in parallel
clear each other's rows.

The migration test creates and drops its own database. It drops a schema to get
a clean slate, and pointing it at a database in use would wipe it.

## The failure suite

Each row is a way the system is expected to break, and the test that proves it
recovers. These are the tests worth running before believing anything else.

| Failure | Proven by |
|---|---|
| Worker crashes mid-task | `TestWorkerDeathMidTaskIsRecovered` — the lease is reclaimed, a second worker finishes the job, and the dead worker's late report is refused |
| A task kills every worker it touches | `TestRepeatedWorkerDeathExhaustsBudget` — budget is consumed at lease time, so it stops rather than touring the fleet |
| Lease expires while work is still running | `TestSweepLeavesLiveLeasesAlone` — a progress report renews it; only a silent worker loses its task |
| Two workers report the same task | `TestStaleAttemptCannotReport`, `TestConcurrentTransitionsHaveOneWinner` — exactly one wins, the rest are refused |
| Two workers poll at the same instant | `TestConcurrentLeasesDoNotCollide` — each gets a different task, none gets two |
| Database restarts underneath us | `TestPoolRecoversFromConnectionLoss` — connections are terminated and the pool makes new ones |
| Control plane restarts | `TestStateSurvivesAControlPlaneRestart` — no scheduling state was in memory, so a fresh process continues the same work |
| Two control planes sweep at once | `TestConcurrentSweepersDoNotDoubleReclaim` — a lease is reclaimed once, not twice |
| Upload is interrupted | `TestInterruptedUploadResumes` — resume asks storage what actually arrived rather than trusting our own record |
| Upload is truncated or mis-sized | `TestCompleteRejectsSizeMismatch` — no source row exists, so nothing can be processed |
| Storage fails during an encode | `TestEncodeTreatsUploadFailureAsTransient` — classified as ours, not the media's, and retried |
| Storage does not have what was reported | `TestRegisterRequiresTheObjectToExist` — an artifact never points at nothing |
| Output is truncated after a successful encode | `TestTruncatedOutputFailsValidation` — size, checksum and playability all fail; the set is marked failed |
| Output bytes are altered | `TestAlteredBytesFailValidation` — only the checksum can see this, and it does |
| A job is cancelled mid-encode | `TestCancelKillsFFmpegPromptly`, `TestCancelLeavesNoStrayProcesses` — the process group dies, nothing is orphaned |
| A worker finishes just as a cancel lands | `TestCompletingACancelledTaskIsNotAnError` — accepted quietly, result discarded |
| A cancelled task's lease expires | `TestExpiryDoesNotResurrectCancelledWork` — work someone stopped is not restarted |
| Media that will never decode | `TestProbeClassifiesUnreadableSourceAsPermanent` — permanent, so it is not retried across the fleet |
| A caller sends a hostile encoding configuration | `TestInjectionAttemptsAreRefused` — refused rather than escaped, against every caller-controlled field |
| A spec names a local path instead of a URL | `TestProbeRejectsNonHTTPSources` — a task cannot become a read of the worker's filesystem |
| A secret reaches a log line | `TestSecretsNeverReachTheLog` — including via `With` and inside groups |
| One tenant reaches another's data | Cross-tenant tests in `auth`, `upload`, `job` and `artifact` |

## The media corpus

```sh
./scripts/testdata.sh          # writes testdata/
```

Fixtures are generated rather than committed: a repository is a poor place for
binaries, and a generator says what a file is supposed to be in a way a
checked-in mp4 never can. The set covers 4K HDR10, ordinary SDR 1080p, a source
too small to fill a ladder, portrait, two audio languages, something that is
not media at all, and a real file cut in half.

## Media tests

Tests that touch media generate their own fixtures with FFmpeg rather than
committing binaries, and skip when it is missing or too old. The version matters:
a developer's FFmpeg is usually newer than the one pinned in the worker image
and will accept flags the deployed build does not, so the pinned build is the
reference and CI checks it has not moved.

## What is not covered yet

- Chunked encoding, packaging and DRM, none of which exist yet.
- A restore from backup. A backup that has never been restored is not a
  verified backup, and this is the gap that matters most before production.
