# Soda BDMS signer

This optional sidecar generates the `X-Helios` and `X-Medusa` headers required
by the current Soda Music PC API. MusicBot sends only the target URL and the
non-sensitive headers that participate in signing; account cookies and audio
downloads never pass through this service.

The image downloads checksum-pinned copies of the official Soda Music 3.7.0
installer and Windows Node.js during its multi-stage build. Only `node.exe`,
`bdms.node`, and `metasecml.dll` are copied into the runtime image. The vendor
binaries are not committed to this repository.

The Compose service has no published host port. `/v1/sign` accepts only HTTPS
targets under `qishui.com`, and an optional bearer token can be enabled with
`SODA_BDMS_TOKEN`. A stable device ID and the Wine prefix are kept in the
`soda-bdms-data` volume.

This image currently supports Linux/amd64 only because the vendor addon is a
Windows x64 binary.
