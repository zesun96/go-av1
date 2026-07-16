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
2. Select **Camera**, the desired camera and a resolution preset, or select
   **Shared desktop**.
3. Click **Start stream**. Desktop capture opens the browser's screen, window,
   or tab picker.
4. The browser AV1-encodes the selected source and sends it over WebRTC.
5. The server reassembles RTP packets into AV1 temporal units (RFC 9321), writes
   IVF, and feeds the units to the go-av1 decoder.
6. Click **Stop stream**, or use the browser's stop-sharing control, to end the
   session.

When the track ends, the server finalizes and closes both output files. Wait for
the `YUV output finalized` log entry before opening the completed Y4M file.
Pressing `Ctrl+C` also closes active WebRTC connections, waits for recording
finalization, and then shuts down the HTTP server. Output close does not force a
full disk-cache sync, avoiding a long pause for large Y4M files while still
writing the final container headers before the files are closed.

The source controls are locked while streaming because Y4M has fixed dimensions
for the complete file. Stop the current stream before selecting another camera
or switching between camera and desktop capture.

For camera capture, the sender requests WebRTC's `maintain-resolution`
degradation preference. Desktop capture keeps the browser's adaptive sender
behaviour. If the browser changes encoded resolution, the server keeps IVF as
one complete stream and starts a new valid Y4M segment (`output-001.y4m`,
`output-002.y4m`, and so on) instead of mixing different frame sizes in one
Y4M file.

Camera labels may be hidden until camera permission is granted. The page
refreshes the device list after permission succeeds and when devices change.
Selected camera resolution presets use exact width and height constraints, so
unsupported camera/driver combinations fail visibly instead of silently using
another size. The page logs the actual output size and the capability range
reported by the browser. Web APIs expose width and height ranges rather than an
exhaustive list of discrete hardware modes, so the menu contains explicit
common presets.

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
- IVF uses WebRTC's 90 kHz video RTP clock and preserves each temporal unit's
  RTP presentation timestamp
- Y4M has no per-frame timestamps, so its fixed frame-rate header is finalized
  from the first and last RTP timestamps to preserve the recording duration
- RFC 9321 aggregation and fragmented OBU reassembly are supported
- OBU size fields are restored before units are written to IVF
