## What's Changed

### Bug Fixes
- **googlephotos**: Fix Google Photos Picker selection showing empty names / 0B and failing with "picker path missing media id or base_url". The Picker `mediaItems` payload nests `baseUrl`/`mimeType`/`filename` under `mediaFile`; the previous flat decoding left them blank. Items now resolve with correct name, type and download URL.

### Other
- **frontend**: Hide the size badge for Google Photos source items whose size is unknown (0B) instead of displaying "0 B".
