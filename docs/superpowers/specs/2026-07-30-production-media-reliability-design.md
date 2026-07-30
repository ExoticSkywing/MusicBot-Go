# Production Media Reliability Design

## Goal

Repair the production-only Kuwo and Kugou failures discovered after the first
Kuwo deployment, while preserving the existing quality boundaries, download
integrity checks, and the separation between the standard and Pro histories.

## Confirmed failures

### Kuwo

For track `165721002`, the verified detail is free and 226 seconds long.

- The optional third-party Hi-Res resolver is unstable and has returned HTTP
  errors, timeouts, downgraded quality, and (in production) a mismatched RID.
  A mismatched candidate is rejected correctly, but treating that rejection as
  fatal prevents the independent official 2000-FLAC resolver from running.
- The official 2000 selector returns a complete stereo FLAC for the requested
  RID. Its STREAMINFO is 24-bit/48 kHz, so the current exact 16-bit allow-list
  rejects a usable non-master stream.
- From the Hong Kong production egress, all tested official mobile-selector
  variants return HTTP 200 with business code 407 before a CDN URL is issued.
  The same container can fetch the Kuwo CDN object normally, so this is a
  selector-region failure rather than a media-delivery failure.
- The same production object contains every STREAMINFO-declared sample and a
  valid final frame, followed by a 30-byte vendor trailer. The existing
  downloader recognizes only an older 15-byte trailer, so strict decoders
  report `LOST_SYNC` after playing all samples.
- The mobile 320 response is an unrelated 11-second preview. The mobile 128
  response is AAC despite the MP3 request. The independently verified Web
  fallback returns a valid standard MP3, but the earlier candidate rejection
  prevents reaching it.
- After the lossless repair was tested in the Hong Kong container, a High
  request still reached only a verified standard MP3 because the official
  mobile selector remained unavailable from that egress. The independent
  resolver's `level=exhigh` response provides the requested 320 MP3 there.
  Its `level=standard` response is currently an approximately 100 kbps AAC, so
  it must not be wired as, or relabeled to, standard 128 MP3.

The repair keeps wrong-track, preview, and unsafe URLs unservable. Only a RID
mismatch from the optional Hi-Res resolver or a mobile candidate may be
discarded so the next resolver can restart from the already verified original
track RID. Mobile format and bitrate must be validated before its URL so an
obvious tier mismatch can be downgraded without interpreting an irrelevant URL.

The official 2000 selector may return either:

- stereo 16-bit at 44.1/48 kHz as lossless; or
- stereo 24-bit at 44.1/48 kHz as Hi-Res.

It must continue to reject sample rates above 48 kHz on this selector. The
runtime still never requests `jymaster`, encrypted master formats, or any
master/super-resolution tier.

When the official 2000 selector is unavailable, the existing independent
quality resolver may be queried with `level=lossless`. This fallback must
return the requested RID, matching duration, requested and actual
`lossless`, bitrate 2000, an empty encryption key, a matching lossless quality
entry, a bounded declared size, and an allow-listed Kuwo CDN URL. Its media is
then subjected to the same Range, STREAMINFO, trailer-boundary, full-frame,
sample-count, and PCM-MD5 checks as every other direct FLAC. Only stereo
16/24-bit 44.1/48 kHz is accepted; 16-bit is reported as Lossless and 24-bit
as Hi-Res. Hi-Res resolution therefore tries independent Hi-Res, official
2000, then this independently resolved lossless stream. Lossless resolution
tries official 2000, then independently resolved lossless.

High resolution first tries the independent `level=exhigh` candidate, then
retains the existing mobile 320, mobile 128, and Web fallback order. The
independent response must provide the requested RID and duration, requested
and actual `exhigh`, bitrate 320, an empty encryption key, a matching
320/MP3/exhigh quality entry, a bounded declared size, and an allow-listed
Kuwo CDN `.mp3` URL. A Range probe must then find a real MPEG frame and verify
an average bitrate between 256 and 384 kbps plus a matching media size.
Standard quality does not call the independent resolver and keeps its existing
verified MP3 paths.

Vendor trailers are not part of the native FLAC stream. Their shared envelope
is only a candidate-boundary hint, never sufficient authority to truncate
audio. Resolution probes a bounded tail window and accepts a clean EOF first.
Otherwise, a trailer boundary is accepted only when the preceding last frame
has a valid canonical header, header CRC-8, frame CRC-16, format fields matching
STREAMINFO, and a fixed- or variable-block sample endpoint exactly equal to
STREAMINFO's total sample count. The full download rechecks the same tail bytes
and boundary proof before truncating the unpublished temporary file. Before
publication, the cleaned file is decoded frame by frame: every frame CRC,
blocking sequence, total sample count, and STREAMINFO PCM MD5 must pass. This
detects audio-frame corruption or PCM changes outside the probed head and
tail; metadata-only rewrites or bit-different encodings of identical PCM are
intentionally not treated as byte-identity failures. Unknown, ambiguous,
oversized, changed, or incomplete streams fail closed. Progress is
monotonic across retries and reaches 100% only after the synced file is
atomically published; probe and full-download requests also have hard lifetime
bounds.

### Kugou

Gateway metadata exposes a distinct hash and size for standard, high,
lossless, and Hi-Res. The current code stores only the standard size, then
clones it when resolving a higher-quality hash. Production therefore compared
the correct 36-46 MB Hi-Res resources against unrelated 2-3 MB MP3 sizes.

Each plan must bind hash, size, format, and bitrate from the same quality tier.
If a size is absent, it remains zero so the downloader's HEAD response is
authoritative. The global integrity check remains strict.

The separate `errcode=20028` / “需要验证” response is a device-risk-control
failure. Public implementations require a registered DFID. The client already
has the registration protocol, but it treats any complete persisted device as
fresh forever. On this response it will force one device re-registration and
retry the same quality once. A second verification response is classified and
stops further quality fan-out instead of hammering every tier or hiding the
reason behind an unrelated fallback response.

### Telegram logging

The custom telego logger replaced telego's default token-redacting logger. A
transport error can therefore include `/bot<TOKEN>/...` in both telego's own
message and errors later logged by application code.

The telego adapter will redact the configured token, and the project logger
will apply the same exact-secret replacement to messages and structured
attributes as a final defense. Empty secrets are ignored.

## Validation

- Unit tests lock every changed fallback and metadata rule before production
  code changes.
- Kuwo live verification covers the failing RID and confirms the downloaded
  FLAC size, total samples, `flac -t`, and a full ffmpeg decode.
- Kugou live verification covers the two exact size-mismatch hashes and the
  two verification-failure hashes without logging signed URLs or credentials.
- Full Go tests, race tests, vet, and diff checks run before integration.
- Standard `main` is pushed first. Only the focused new standard commits are
  cherry-picked with `-x` onto Pro; no Pro-only history enters standard.
- Production logs are queried through a sanitizer after deployment.
