# go-av1enc

`go-av1enc` is the reference go-av1 encoder CLI. It reads an 8-bit YUV420 Y4M
file and writes an AV1 IVF file.

## Build

From the repository root:

```sh
go build -o go-av1enc ./cmd/go-av1enc
```

On Windows PowerShell:

```powershell
go build -o go-av1enc.exe ./cmd/go-av1enc
```

## Constant-quality encoding

```sh
./go-av1enc -i input.y4m -o output.ivf -crf 30 -preset 12
```

Windows example:

```powershell
.\go-av1enc.exe `
  -i test/data/akiyo_qcif.y4m `
  -o output.ivf `
  -crf 30 `
  -preset 13
```

Lower CRF values retain more quality and usually produce larger files.

Preset 12 is the default quality-oriented preset and performs extensive
multi-reference, partition, compound, and reconstruction-domain motion search.
It is intentionally slow in the current single-threaded encoder. Preset 13 is
recommended for a much faster first encode; it uses a smaller integer-motion
search at the cost of compression efficiency.

## Rate control

Variable bitrate targeting an average of 500 kbit/s:

```sh
./go-av1enc -i input.y4m -o vbr.ivf -preset 13 -rc vbr -b 500
```

Constant bitrate with a one-second virtual buffer:

```sh
./go-av1enc -i input.y4m -o cbr.ivf -preset 13 -rc cbr -b 500
```

Constant AV1 qindex:

```sh
./go-av1enc -i input.y4m -o cqp.ivf -preset 13 -rc cqp -qp 120
```

## Options

| Flag | Default | Description |
|---|---:|---|
| `-i` | required | Input Y4M file |
| `-o` | required | Output IVF file |
| `-crf` | `30` | Constant rate factor, from 0 through 63 |
| `-preset` | `12` | Speed preset, where 0 is slowest and 13 is fastest |
| `-rc` | `crf` | Rate control: `crf`, `cqp`, `vbr`, or `cbr` |
| `-b` | `0` | VBR/CBR target bitrate in kbit/s |
| `-qp` | `120` | CQP AV1 qindex, from 1 through 255 |
| `-obmc` | `false` | Enable overlapped block motion compensation |

## Verify the output

Decode the IVF file with go-av1:

```sh
go run ./cmd/go-av1d -i output.ivf -o decoded.y4m
```

Or inspect it with FFmpeg:

```sh
ffmpeg -i output.ivf -f null -
```

## Current limitations

- 8-bit 4:2:0 input;
- one tile and one encoding thread;
- preset 12 favors compression efficiency over speed;
- 10/12-bit, 4:2:2, and 4:4:4 encoding are not yet implemented.
