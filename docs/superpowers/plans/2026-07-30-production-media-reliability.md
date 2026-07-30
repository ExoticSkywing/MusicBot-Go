# Production Media Reliability Implementation Plan

> Execute each task test-first. Do not weaken media identity, URL, or download
> integrity checks merely to make a fixture pass.

**Goal:** Restore Kuwo FLAC and safe fallback behavior, correct Kugou
quality-specific metadata and device verification recovery, and prevent
Telegram bot tokens from entering logs.

**Architecture:** Keep resolver-specific validation inside each provider.
Permit fallback only by discarding a failed candidate and rebuilding the next
request from verified track metadata. Bind Kugou quality metadata as an atomic
plan. Redact the Telegram token both at the telego adapter and project logger.

**Tech stack:** Go, `net/http`, `log/slog`, provider contract fixtures,
opt-in live E2E tests, Docker/GitHub Actions.

---

## Task 1: Kuwo production fallback and 2000-FLAC profiles

**Files:**

- Modify: `plugins/kuwo/media.go`
- Modify: `plugins/kuwo/direct_hires.go`
- Modify: `plugins/kuwo/playable_lossless.go`
- Test: `plugins/kuwo/media_test.go`
- Test: `plugins/kuwo/direct_hires_test.go`
- Test: `plugins/kuwo/playable_lossless_test.go`

1. Add failing tests showing that a mismatched RID from the optional Hi-Res
   resolver falls through to a verified official 2000 FLAC, and that a
   mismatched mobile RID can continue to an independent valid candidate.
2. Add a failing production-shape test where mobile 320 is a wrong preview,
   mobile 128 reports AAC/100, and Web returns a valid standard MP3.
3. Add a failing 2000-selector test accepting stereo 24-bit/48 kHz while
   preserving rejection of 24-bit/96 kHz and master requests.
4. Implement the smallest resolver-aware fallback policy and validate mobile
   tier metadata before URL normalization.
5. Run `go test ./plugins/kuwo -count=1`.

## Task 1b: Kuwo direct-FLAC boundary integrity

**Files:**

- Modify: `plugins/kuwo/direct_flac_download.go`
- Test: `plugins/kuwo/direct_flac_download_test.go`
- Test: `plugins/kuwo/e2e_test.go`

1. Add a real STREAMINFO/final-frame fixture for clean EOF, the historical
   15-byte trailer, and the production 30-byte trailer.
2. Add failing tests for header CRC-8, frame CRC-16, fixed/variable sample
   endpoints, malformed canonical frame numbers, unknown tails, ambiguous
   boundaries, bounded probes, and probe/download changes.
3. Treat the vendor prefix/suffix only as a candidate boundary. Authorize
   truncation only after a bounded final-frame proof closes exactly at
   STREAMINFO's total sample count.
4. Persist the probed tail range digest and re-run the proof against the full
   temporary download before truncating, syncing, and atomically publishing it.
5. Decode the cleaned temporary file frame by frame and require CRC,
   fixed/variable sample continuity, total samples, and PCM MD5 to match
   STREAMINFO. Keep progress monotonic and reserve 100% for successful atomic
   publication; bound probe and full-download request lifetimes.
6. Run the exact production RID through `flac -t` and a full ffmpeg decode.

## Task 1c: Kuwo regional lossless and High fallbacks

**Files:**

- Modify: `plugins/kuwo/media.go`
- Modify: `plugins/kuwo/direct_hires.go`
- Test: `plugins/kuwo/media_test.go`
- Add: `plugins/kuwo/direct_lossless_test.go`
- Add: `plugins/kuwo/direct_high_test.go`

1. Reproduce the Hong Kong production condition where the official selector
   returns business code 407 while its CDN remains reachable.
2. Add failing tests for Lossless and Hi-Res resolver order and for successful
   external `level=lossless` fallback without MP3.
3. Require exact RID, duration, lossless level, 2000 bitrate, empty encryption
   key, quality entry, declared size, safe CDN URL, and verified FLAC content.
4. Accept only stereo 16/24-bit 44.1/48 kHz, reject higher rates, 32-bit,
   multichannel, and every master/super-resolution request.
5. Reproduce the remaining Hong Kong High-to-Standard downgrade, add
   independent `level=exhigh` before the existing mobile chain, and verify its
   exact contract plus a real 256–384 kbps MP3 probe. Do not use the
   independent `level=standard` AAC response as MP3.
6. Treat a successful resolver envelope with missing identity data as terminal
   while keeping an explicit non-200 business response optional.
7. Run the complete Kuwo E2E suite inside the Hong Kong container before and
   after deployment.

## Task 2: Kugou quality metadata and verification recovery

**Files:**

- Modify: `plugins/kugou/client.go`
- Modify: `plugins/kugou/concept_client.go`
- Modify: `plugins/kugou/concept_types.go`
- Test: `plugins/kugou/client_test.go`
- Test: `plugins/kugou/concept_test.go`

1. Extend gateway/search fixtures to assert the exact size attached to every
   quality hash.
2. Add a failing resolver test proving that a Hi-Res clone never inherits the
   original standard size, bitrate, or format, and that missing tier size stays
   zero.
3. Store per-tier sizes alongside per-tier hashes and apply plan metadata
   atomically before response-specific overrides.
4. Add a failing device-verification test: first `/v5/url` response requests
   verification, forced `register/dev` returns a fresh DFID, and the one retry
   succeeds. Add a second test where verification persists and further quality
   plans are not attempted.
5. Implement one bounded refresh/retry and a typed verification error.
6. Run `go test ./plugins/kugou ./bot/download -count=1`.

## Task 3: Telegram token redaction

**Files:**

- Modify: `bot/telegram/bot.go`
- Modify: `bot/logger/logger.go`
- Modify: `bot/app/app.go`
- Test: `bot/telegram/bot_test.go`
- Add: `bot/logger/logger_test.go`

1. Add failing Debugf/Errorf tests using a synthetic Telegram token and a
   capture logger.
2. Add failing project-logger tests for message text, string attributes,
   `error` attributes, JSON/text output, and child loggers.
3. Give all three telego clients a token-aware adapter.
4. Add exact-secret redaction to the project logger and initialize it with
   `BOT_TOKEN`.
5. Run `go test ./bot/telegram ./bot/logger ./bot/app -count=1`.

## Task 4: Integrated and live verification

1. Run focused tests for all three tasks.
2. Run `gofmt` on changed Go files.
3. Run `go test ./... -count=1`.
4. Run `go test -race ./... -count=1`.
5. Run `go vet ./...`.
6. Run `git diff --check`.
7. Run sanitized Kuwo and Kugou live checks for the exact production tracks;
   inspect media properties without printing URLs, cookies, signatures, or
   tokens.
8. Obtain an independent code review and resolve all critical/important
   findings.

## Task 5: Isolated dual-repository release

1. Recheck both worktrees, remotes, and upstream drift.
2. Fast-forward standard `main` to the reviewed feature commits and push.
3. Wait for standard CI and verify the remote SHA.
4. Cherry-pick only the focused standard commits with `-x` onto Pro.
5. Test Pro, push `pro-main:main`, wait for Pro CI, and verify its remote SHA.
6. Confirm the deployed image revision, then use sanitized logs and live media
   checks to verify the production repair.
