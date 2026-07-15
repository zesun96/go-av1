# webrtc-av1d

A WebRTC server that receives AV1 video from a browser camera or shared
desktop, writes the raw bitstream to IVF, and decodes each frame through the
go-av1 pipeline in real time.

## Module isolation

`webrtc-av1d` has its own `go.mod`
(`github.com/zesun96/go-av1/cmd/webrtc-av1d`) so its
[pion/webrtc](https://github.com/pion/webrtc) dependency does not propagate to
users of the core `github.com/zesun96/go-av1` library.

## Requirements

- Go 1.22+
- A browser with AV1 WebRTC encoding support (current Chrome or Firefox)
- `ffplay` (optional, for playback verification)

Media capture works on `localhost`. Access from another machine normally
requires serving the page over HTTPS because camera and desktop capture are
secure-context browser APIs.

## Build and run

```sh
cd cmd/webrtc-av1d

go run . -port 8080 -out output.ivf -yuv output.y4m

# Or build first.
go build -o webrtc-av1d .
./webrtc-av1d -port 8080 -out output.ivf -yuv output.y4m
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `-port` | `8080` | HTTP listen port |
| `-out` | `output.ivf` | Output IVF file path |
| `-yuv` | `output.y4m` | Decoded Y4M or raw YUV output path |

## Capture workflow

1. Start the server and open `http://localhost:<port>`.
2. Select **Camera** and the desired camera, or select **Shared desktop**.
3. Click **Start stream**. Desktop capture opens the browser's screen, window,
   or tab picker.
4. The browser AV1-encodes the selected source and sends it over WebRTC.
5. The server reassembles RTP packets into AV1 temporal units (RFC 9321), writes
   IVF, and feeds the units to the go-av1 decoder.
6. Click **Stop stream**, or use the browser's stop-sharing control, to end the
   session.

The source controls are locked while streaming because Y4M has fixed dimensions
for the complete file. Stop the current stream before selecting another camera
or switching between camera and desktop capture.

Camera labels may be hidden until camera permission is granted. The page
refreshes the device list after permission succeeds and when devices change.

## Playback

```sh
ffplay output.y4m

# Convert decoded frames to WebM if needed.
ffmpeg -i output.y4m -c:v libaom-av1 -b:v 2M -cpu-used 4 \
  -row-mt 1 -tiles 2x2 output.webm
```

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Browser capture frontend |
| `POST` | `/offer` | WebRTC SDP signaling with a JSON offer |

## IVF and RTP notes

- IVF codec fourcc: `AV01`
- Time base: 1/30 second per frame
- RFC 9321 aggregation and fragmented OBU reassembly are supported
- OBU size fields are restored before units are written to IVF
