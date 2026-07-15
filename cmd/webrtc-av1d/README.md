# webrtc-av1d

A WebRTC server that receives AV1 video streamed from a browser, writes the
raw bitstream to an IVF file, and decodes each frame through the go-av1
pipeline in real time.

## Module isolation

`webrtc-av1d` has its **own `go.mod`** (`github.com/zesun96/go-av1/cmd/webrtc-av1d`)
so that its [pion/webrtc](https://github.com/pion/webrtc) dependency does not
propagate to consumers of the core `github.com/zesun96/go-av1` library.

## Requirements

- Go 1.22+
- A browser with AV1 WebRTC encoding support (Chrome 112+, Firefox 113+)
- [ffplay](https://ffmpeg.org/) (optional, for playback verification)

## Build & run

```sh
cd cmd/webrtc-av1d

# Run directly
go run . -port 8080 -out output.ivf -yuv output.y4m

# Or build first
go build -o webrtc-av1d .
./webrtc-av1d -port 8080 -out output.ivf -yuv output.y4m
```

## Flags

| Flag | Default | Description |
|---|---|---|
| `-port` | `8080` | HTTP listen port |
| `-out` | `output.ivf` | Output IVF file path |
| `-yuv` | `output.y4m` | Output YUV file path |

## Workflow

1. Start the server.
2. Open `http://localhost:<port>` in Chrome.
3. Click **Start Stream** — the browser requests camera access, negotiates
   WebRTC, and begins sending AV1-encoded video.
4. The server reassembles RTP packets into AV1 temporal units (RFC 9321),
   writes each unit to the IVF file, and feeds it to the go-av1 decoder.
5. Click **Stop Stream** (or close the tab) to end the session.
6. The server prints a summary (`total frames decoded: N`) and exits.

## Play back the recorded file

```sh
# ffplay supports IVF natively
ffplay output.y4m

# Convert to WebM for VLC / other players (stream copy, lossless)
ffmpeg -i output.y4m -c:v libaom-av1 -b:v 2M -cpu-used 4 -row-mt 1 -tiles 2x2 output.webm
```

> **Note:** VLC does not support the IVF container. Use `ffmpeg` to convert
> to WebM first.

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Serves the browser frontend (`static/index.html`) |
| `POST` | `/offer` | WebRTC SDP signalling — JSON body `{"sdp":"…","type":"offer"}` |

## IVF output format

- **Codec:** AV1 (`AV01` fourcc)
- **Time base:** 1/30 s per frame (frame-counter PTS)
- **Container:** minimal IVF (32-byte file header + per-frame 12-byte headers)
- Each frame payload contains one or more complete AV1 OBUs with
  `obu_has_size_field=1` as required by the AV1 bitstream specification.

## Protocol notes (RFC 9321)

- **Aggregation header:** Z/Y fragment bits and W element-count field are
  fully handled; fragmented OBUs are reassembled before writing.
- **OBU size field:** ensured present (`obu_has_size_field` bit set) on every
  OBU before writing to the IVF container.
