<div align="center">
  <img src="assets/icon.png" alt="" width="128" height="128">
  <h1>Kotori</h1>
  <p>小鳥 — <em>little bird</em></p>
</div>

---

A command-line downloader for Anikoto, written in Go.

It is a single binary with a built-in HLS downloader, so it does not need
yt-dlp, aria2c or N_m3u8DL-RE.

> **Status: early.** The MegaPlay path (the `hd` and `vidstream` servers) has
> been run end to end against the live site. The VidTube, `save_data.php` and
> Kiwi paths have only been tested offline.

## Install

Requires Go 1.25 or newer to build, and [ffmpeg](https://ffmpeg.org) on your
`PATH` to run. kotori checks for ffmpeg before it downloads anything.

```sh
go install github.com/brysonreece/kotori@latest
```

Or from a checkout:

```sh
go build -o kotori .
```

## Usage

```sh
kotori [flags] <series>
```

The series is the slug from its address on the site. For
`https://anikototv.to/watch/dragon-ball-gxrfm/ep-1`, that is
`dragon-ball-gxrfm`. Pasting the full URL of any of its pages works too; the
episode in the URL is ignored, and `-e` chooses what to download.

```sh
# List the episodes of a series
kotori dragon-ball-gxrfm --list

# Download episodes 1 through 5
kotori dragon-ball-gxrfm -e 1-5

# Download everything, subbed, at up to 720p
kotori dragon-ball-gxrfm -a sub -q 720

# Download episodes 1, 3 and 5 through 8 into ~/Anime as MKV
kotori dragon-ball-gxrfm -e 1,3,5-8 -p ~/Anime -f mkv

# Download only the newest episode
kotori dragon-ball-gxrfm --last

# Paste a URL straight from the browser
kotori https://anikototv.to/watch/dragon-ball-gxrfm/ep-1 -e 1-5
```

| Flag | Default | Meaning |
| --- | --- | --- |
| `-q`, `--quality` | `1080` | Preferred resolution: 2160, 1440, 1080, 720, 480 or 360. Picks the best rendition at or below it. |
| `-a`, `--audio` | `dub` | `sub` or `dub`. |
| `-f`, `--format` | `mp4` | Output format: `mp4`, `mkv`, `mov`, `avi` or `webm`. See [Output formats](#output-formats). |
| `-s`, `--source` | all | Comma-separated servers to try: `megaplay`, `vidstream`, `kiwi`, `vidcloud`, `vidplay`, `hd`. |
| `-e`, `--episodes` | all | Episode numbers and ranges, e.g. `1,3,5-8`. |
| `--last` | | Download only the latest episode. |
| `--list` | | List episodes and exit. |
| `-p`, `--path` | `.` | Directory to save into. |
| `-b`, `--base-url` | `https://anikototv.to` | Site address to use. See [When the site moves](#when-the-site-moves). |
| `--no-subtitles` | | Skip subtitle downloads. |
| `-l`, `--subtitle-lang` | `English` | Subtitle language, matched against track labels. |
| `-c`, `--concurrency` | `8` | Segments to download at once. |
| `--debug` | | Print every request and every skipped server. |

Files are saved as:

```
<path>/<Series>/<Series> E01 <Episode title>.mp4
<path>/<Series>/<Series> E01 <Episode title>.en.vtt
```

Episodes that already exist in the requested format are skipped. An
interrupted download keeps its finished segments in a `.parts` directory and
resumes from there. If the conversion step fails, the raw stream is kept as a
`.download` file and the next run converts it without downloading again.

### When the site moves

Anikoto changes domain every so often. When it does, point kotori at the new
address instead of waiting for an update:

```sh
# One-off
kotori dragon-ball-gxrfm --base-url https://new-domain.example

# For every run
export KOTORI_BASE_URL=https://new-domain.example
```

With a base URL set, it replaces the scheme and host of whatever you pass, so
old bookmarks and links to a previous domain keep working. Without one, a full
URL is used exactly as given, and a slug goes to the built-in default.
The flag takes priority over the environment variable.

### Output formats

| Format | How it is made |
| --- | --- |
| `mp4`, `mkv`, `mov` | The stream is rewrapped as it is. This is fast and loses no quality. |
| `avi` | Re-encoded to MPEG-4 video and MP3 audio. |
| `webm` | Re-encoded to VP9 video and Opus audio. |

Re-encoding is much slower than rewrapping, loses some quality, and needs an
ffmpeg build with the matching encoders (`libmp3lame`, or `libvpx` and
`libopus`). Prefer `mp4` or `mkv` unless a device needs something else.

kotori exits with status 1 if any episode failed and 2 for a usage error.

## How it works

1. **Series page.** The series page gives the series ID and title, and an AJAX
   call returns the episode list.
2. **Servers.** Each episode offers several servers, grouped by sub and dub.
   kotori tries the ones matching `--audio` and `--source`, in order, until
   one downloads.
3. **Stream.** Each server leads to an embedded player. Three player backends
   are understood (MegaPlay, VidTube and a `save_data.php` variant), plus the
   Kiwi mapper as a last resort.
4. **Download.** The HLS playlist is fetched, a rendition chosen, and the
   segments downloaded concurrently and joined.
5. **Convert.** ffmpeg writes the joined stream into the requested format.

Some of that takes extra work:

- **Encrypted MegaPlay sources.** The key, IV and signing secret are read out
  of the obfuscated player script by static analysis. The script is only ever
  treated as text and is never executed.
- **Signed CDN URLs.** The player's HMAC token is reproduced for manifest
  URLs that need one.
- **Obfuscated segment URLs.** Some CDNs hide each real segment URL inside an
  encrypted path. These are decrypted, and filler entries are dropped.
- **Fake PNG headers.** Segments wrapped in a decoy PNG image are unwrapped.

## Behaviour

- Downloading is built in; there is no yt-dlp, aria2c or N_m3u8DL-RE
  dependency. ffmpeg is required, for the final conversion.
- `--format` chooses the output container.
- The cleaned-up playlist is used in memory; nothing is uploaded anywhere.
- `--episodes` accepts numbers and ranges, and always refers to real episode
  numbers.
- Subtitles are downloaded by default; `--no-subtitles` turns them off.
- `--quality` falls back to the next rendition down when there is no exact
  match.
- A segment that keeps failing fails the episode, so no file with a hole in
  it is written.
- When the CDN answers `429 Too Many Requests`, every download worker pauses
  and then retries. Lower `--concurrency` if you see a lot of pauses under
  `--debug`.
- Episode numbers are zero-padded so files sort correctly.

## Limitations

- Streams that carry audio as a separate HLS rendition are not supported.
- Only AES-128 segment encryption is supported, not SAMPLE-AES.
- Requests use Go's standard TLS stack. If the site starts fingerprinting
  clients, they may be blocked.

## Development

```sh
go test ./...
```

The tests run offline against local test servers. The conversion tests
generate a real stream with ffmpeg and are skipped when it is not installed.

```
main.go               entry point
internal/cli          flags, episode selection, orchestration
internal/anikoto      site scraping and stream resolution
internal/megaplay     player script analysis, source decryption, URL signing
internal/hls          playlist parsing and the segment downloader
internal/ffmpeg       conversion to the output format
internal/fetch        shared HTTP client
internal/aescbc       AES-CBC helper
```

## Disclaimer

This is a personal project and is not affiliated with Anikoto. You are
responsible for making sure your use complies with the laws that apply to
you and with the rights of the content's owners.
