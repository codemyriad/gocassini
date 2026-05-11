# D-263 Useful Links

This file collects the Nextcloud-side references that are useful for the
`Native Talk UX + Cassini backend` implementation path.

It is intentionally biased toward:

- official documentation
- official source repositories
- the concrete source files that shape the Talk recording-backend contract

## Product and user docs

- Nextcloud Talk call recording user manual
  - https://docs.nextcloud.com/server/latest/user_manual/nn/talk/call_recording.html
  - useful for confirming the expected moderator UX and that recording requires admin setup

- Nextcloud Talk user manual index
  - https://docs.nextcloud.com/server/latest/user_manual/en/talk/index.html

## Talk recording API docs

- Nextcloud Talk recording API
  - https://nextcloud-talk.readthedocs.io/en/stable/recording/
  - main protocol reference for:
    - start/stop endpoints
    - backend callbacks
    - `/store` upload semantics
    - request signing rules

## Nextcloud developer docs

- Talk integration developer manual
  - https://docs.nextcloud.com/server/stable/developer_manual/digging_deeper/talk.html

- App settings developer manual
  - https://docs.nextcloud.com/server/stable/developer_manual/basics/setting.html

- HTTP client developer manual
  - https://docs.nextcloud.com/server/27/developer_manual/digging_deeper/http_client.html

## Official recording server

- Official Nextcloud Talk recording server repository
  - https://github.com/nextcloud/nextcloud-talk-recording

- README
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/README.md
  - useful for confirming:
    - it is the official recording server
    - it expects standalone signaling
    - it exposes an HTTP API and typically sits behind reverse-proxy TLS

- Request entrypoint and auth validation
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/src/nextcloud/talk/recording/Server.py

- Recording lifecycle implementation
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/src/nextcloud/talk/recording/Service.py

- Callbacks and `/store` upload behavior
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/src/nextcloud/talk/recording/BackendNotifier.py

- Room-token based join flow
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/src/nextcloud/talk/recording/Participant.py

- Backend/signaling config shape
  - https://github.com/nextcloud/nextcloud-talk-recording/blob/main/src/nextcloud/talk/recording/Config.py

## Nextcloud Talk source

- Talk app repository
  - https://github.com/nextcloud/spreed

- Recording controller
  - https://github.com/nextcloud/spreed/blob/main/lib/Controller/RecordingController.php
  - useful for:
    - backend callback handling
    - `/store` handling
    - authentication rules on the Nextcloud side

- Recording service
  - https://github.com/nextcloud/spreed/blob/main/lib/Service/RecordingService.php
  - useful for:
    - accepted recording file formats
    - how uploaded recordings are stored
    - transcript/summary follow-up behavior in Talk

- Backend notifier
  - https://github.com/nextcloud/spreed/blob/main/lib/Recording/BackendNotifier.php
  - useful for:
    - exact backend request shapes
    - request signing behavior from Talk to the recording backend

- Recording admin settings UI
  - https://github.com/nextcloud/spreed/blob/main/src/components/AdminSettings/RecordingServers.vue
  - useful for understanding what admins configure in Talk

- Talk config
  - https://github.com/nextcloud/spreed/blob/main/lib/Config.php
  - useful for recording server config shape and related capabilities

## Talk recording frontend helpers

- Recording entry app
  - https://github.com/nextcloud/spreed/blob/main/src/mainRecording.js

- WebRTC utilities including recording-specific signaling helpers
  - https://github.com/nextcloud/spreed/blob/main/src/utils/webrtc/index.js
  - useful for:
    - `signalingGetSettingsForRecording`
    - `signalingJoinCallForRecording`
    - confirmation that the official model is base URL + room token, not copied public call URL

## Operational docs

- Recording server installation overview
  - https://portal.nextcloud.com/article/Nextcloud-Talk/Recording-Server/Installation

- High-performance backend installation overview
  - https://portal.nextcloud.com/article/Nextcloud-Talk/High-Performance-Backend/Installation-of-Nextcloud-Talk-High-performance-backend

- Recording server category on the Nextcloud portal
  - https://portal.nextcloud.com/categories/Nextcloud-Talk/Recording-Server

## How to use these references

- Use `nextcloud-talk.readthedocs.io` and `RecordingController.php` as the source of truth for the Talk protocol.
- Use `nextcloud-talk-recording` as the source of truth for:
  - expected callback/upload ordering
  - deployment shape
  - baseURL + roomToken execution model
- Do not treat the official recording server's browser-plus-ffmpeg recorder as the architecture Cassini should copy.
