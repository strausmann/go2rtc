# UniFi Protect

UniFi Protect cameras with a speaker (for example G4 Doorbell Pro) support **talkback** (two-way audio) via the Protect integration API.

The source only adds the audio backchannel. Video and audio from the camera come from the usual RTSP source.

- create an API key for the UniFi Protect integration API
- get the camera ID: `curl -sk -H "X-API-KEY: <api_key>" https://192.168.1.1/proxy/protect/integration/v1/cameras`
- requires FFmpeg with `libopus` and the [`exec`](../exec/README.md) module
- FFmpeg path is taken from the [`ffmpeg`](../ffmpeg/README.md) module config (`ffmpeg: bin:`), if you use `exec: allow_paths:`, add this path there

## Configuration

```yaml
streams:
  doorbell:
    - rtspx://192.168.1.1:7441/<alias>#backchannel=0
    - unifi:https://192.168.1.1?camera_id=<camera_id>&api_key=<api_key>
```

The talkback session is opened only when a client starts sending microphone audio. Normal viewing does not activate the camera speaker.

FFmpeg transcodes the microphone to Opus with the sample rate requested by Protect (24 kHz for G4 Doorbell Pro).

## Microphone codec

By default, the source takes the microphone as PCMA or PCMU (G.711). This is phone quality (8 kHz), but it works with every client.

With `#audio=opus` the source takes the microphone as Opus. This sounds much better, but the client must send Opus (WebRTC clients do).

```yaml
streams:
  doorbell:
    - rtspx://192.168.1.1:7441/<alias>#backchannel=0
    - unifi:https://192.168.1.1?camera_id=<camera_id>&api_key=<api_key>#audio=opus
```
