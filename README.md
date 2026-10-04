<div align="center">
  <img src="assets/icon.png" alt="" width="128" height="128">
  <h1>Kotori</h1>
  <p>小鳥 — <em>little bird</em></p>
</div>

---

A command-line downloader for Anikoto, written in Go.

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
kotori [flags] [series or search terms]
```

Run it with no arguments and kotori walks you through a download:

```
$ kotori
Search
› dragon ball z

Results for "dragon ball z"
› Dragon Ball Z  TV · 291 sub · 291 dub
  Dragon Ball Z Kai  TV · 98 sub · 98 dub
  ...

✓ Show      Dragon Ball Z
✓ Episodes  1-5
✓ Audio     sub
✓ Quality   up to 1080p
✓ Format    mkv
✓ Subtitles srt
✓ Save to   ~/Anime
✓ Names     Dragon Ball Z/Dragon Ball Z E001 The Arrival of Raditz.mkv
```

It asks for the show or movie, then the episodes, audio, quality, output
format, subtitle format, folder and file naming, and starts downloading.

```
Downloading 5 of 153 episodes

[1 / 5]  Bulma and Son Goku  ✓ done  1080p · 294.1 MB
[2 / 5]  What the...?! No Balls!  ✓ done  1080p · 281.7 MB
[3 / 5]  The Turtle Hermit's Kinto Un  ████████████░░░░░░░░░░░░  52%
         hd · 143/276 segments · 146.2 MB · 6.1 MB/s · 0:22 left
```

and the run ends with what was downloaded and where it is:

```
Downloaded 5 episodes · 1.42 GB
Saved to /Users/you/Anime/Dragon Ball
```

Each episode gets a line. The one in progress shows a bar, with the server,
speed and time left beneath it. If an episode fails, the reason from each
server that was tried stays under its line. When the output is not a
terminal, each episode is printed once, when it finishes.

| Key                                 | Action                                          |
| ----------------------------------- | ----------------------------------------------- |
| `↑`/`↓` or `k`/`j`        | Move through a list.                            |
| `PgUp`/`PgDn`, `Home`/`End` | Jump a screen, or to either end.                |
| `enter`                           | Choose, or confirm the ticked episodes.         |
| `space`                           | Tick or untick an episode and move to the next. |
| `a`                               | Tick every episode, or untick them all.         |
| `esc`                             | Go back a step.                                 |
| `ctrl+c`                          | Quit.                                           |

The results and episode lists scroll, and show which row you are on, such as
`12 of 291`. In the episode list, hold `space` to tick a run of episodes.

Anything you give on the command line is not asked for again:

```sh
# Search for a title, then answer the rest
kotori dragon ball z

# Skip the search by naming the series; only the settings are asked
kotori dragon-ball-gxrfm

# Also skip the quality and format questions
kotori dragon-ball-gxrfm -q 720 -f mkv

# Ask nothing: use the defaults for whatever is not given
kotori dragon-ball-gxrfm -e 1-5 -y

# Give everything, and nothing is asked either
kotori dragon-ball-gxrfm -e 1-5 -a sub -q 720 -f mkv --subtitle-format srt \
  -p ~/Anime -o "{series}/{series} E{episode} {title}"
```

The series can be named three ways:

- its **slug**, the last part of its address on the site. For
  `https://anikototv.to/watch/dragon-ball-gxrfm/ep-1`, that is
  `dragon-ball-gxrfm`.
- the **URL** of any of its pages. The episode in the URL is ignored, and
  `-e` chooses what to download.
- **search terms**. Several words always search. A single word is tried as a
  slug first and searched for if no series has it.

Steps that have nothing to decide are skipped: a movie has no episode
question, and a series with only subbed or only dubbed episodes has no audio
question.

When kotori is not running in a terminal, such as in a script or with its
output piped, it never asks anything. It uses the defaults, and it needs a
slug or URL since it cannot show search results.

```sh
# List the episodes of a series
kotori dragon-ball-gxrfm --list

# Download only the newest episode
kotori dragon-ball-gxrfm --last -y
```

| Flag                        | Default                                  | Meaning                                                                                                  |
| --------------------------- | ---------------------------------------- | -------------------------------------------------------------------------------------------------------- |
| `-q`, `--quality`       | `1080`                                 | Preferred resolution: 2160, 1440, 1080, 720, 480 or 360. Picks the best rendition at or below it.        |
| `-a`, `--audio`         | `dub`                                  | `sub` or `dub`.                                                                                      |
| `-f`, `--format`        | `mp4`                                  | Output format:`mp4`, `mkv`, `mov`, `avi` or `webm`. See [Output formats](#output-formats).      |
| `-s`, `--source`        | all                                      | Comma-separated servers to try:`megaplay`, `vidstream`, `kiwi`, `vidcloud`, `vidplay`, `hd`. |
| `-e`, `--episodes`      | all                                      | Episode numbers and ranges, e.g.`1,3,5-8`.                                                             |
| `--last`                  |                                          | Download only the latest episode.                                                                        |
| `--list`                  |                                          | List episodes and exit.                                                                                  |
| `-p`, `--path`          | `.`                                    | Directory to save into.                                                                                  |
| `-o`, `--output`        | `{series}/{series} E{episode} {title}` | File name pattern. See[File names](#file-names).                                                          |
| `-b`, `--base-url`      | `https://anikototv.to`                 | Site address to use. See[When the site moves](#when-the-site-moves).                                      |
| `--subtitle-format`       | `vtt`                                  | Subtitle format:`vtt`, `srt` or `ass`.                                                             |
| `--no-subtitles`          |                                          | Skip subtitle downloads.                                                                                 |
| `-l`, `--subtitle-lang` | `English`                              | Subtitle language, matched against track labels.                                                         |
| `-j`, `--jobs`          | `4`                                    | Episodes to download at once.                                                                            |
| `-c`, `--concurrency`   | `8`                                    | Segments to download at once, per episode.                                                               |
| `-y`, `--yes`           |                                          | Don't ask for settings; use the defaults for anything not given.                                         |
| `--debug`                 |                                          | Print every request and every skipped server.                                                            |

### File names

`--path` is the folder everything goes in, and `--output` is the pattern for
each file inside it. The default pattern gives:

```
<path>/<Series>/<Series> E01 <Episode title>.mp4
<path>/<Series>/<Series> E01 <Episode title>.en.vtt
```

A pattern is a relative path without the extension. A `/` starts a folder,
and these placeholders are filled in for each episode:

| Placeholder   | Becomes                                                  |
| ------------- | -------------------------------------------------------- |
| `{series}`  | The show or movie title.                                 |
| `{episode}` | The episode number, zero-padded so files sort correctly. |
| `{title}`   | The episode title.                                       |
| `{audio}`   | `sub` or `dub`.                                      |

```sh
# Plex and Jellyfin naming
kotori dragon-ball-gxrfm -o "{series}/Season 01/{series} - S01E{episode} - {title}"

# Everything in one folder
kotori dragon-ball-gxrfm -o "{series} E{episode} {title}"

# Keep subbed and dubbed copies apart
kotori dragon-ball-gxrfm -o "{series} ({audio})/{series} E{episode} {title}"
```

The pattern must include `{episode}`, or every episode would be written to
the same file. Characters a file name cannot hold are replaced with `_`, so a
title never creates a folder by accident. In the wizard, the file names step
offers these patterns ready-made, each shown with the name it would produce,
plus a choice to write your own.

Subtitles are saved beside the video, named for their language. The site
provides WebVTT (`.vtt`); `--subtitle-format srt` or `ass` converts them with
ffmpeg, for players that do not read WebVTT. If a conversion fails, the
original is kept.

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

| Format                    | How it is made                                                       |
| ------------------------- | -------------------------------------------------------------------- |
| `mp4`, `mkv`, `mov` | The stream is rewrapped as it is. This is fast and loses no quality. |
| `avi`                   | Re-encoded to MPEG-4 video and MP3 audio.                            |
| `webm`                  | Re-encoded to VP9 video and Opus audio.                              |

Re-encoding is much slower than rewrapping, loses some quality, and needs an
ffmpeg build with the matching encoders (`libmp3lame`, or `libvpx` and
`libopus`). Prefer `mp4` or `mkv` unless a device needs something else.

kotori exits with status 1 if any episode failed, 2 for a usage error, and
130 if you quit the questions with `ctrl+c`.

## Speed

The video CDNs allow about 100 requests per 10 seconds per domain, and block
for about 11 seconds when that is exceeded. One episode lives on one domain,
so a single episode cannot go faster than roughly ten segments a second, which
is about 30 seconds for a 24-minute episode.

Different episodes are spread across several CDN domains, each with its own
allowance. kotori uses that in two ways:

- **Episodes download in parallel** (`--jobs`, 4 by default). Episodes on
  different domains do not slow each other down. Eight episodes that took
  about five minutes one at a time take about two minutes at four at once.
- **Requests are paced per domain.** Nothing is assumed until a domain
  refuses a request with `429 Too Many Requests`. Its `Retry-After` and the
  number of requests it had just served give its limit, and from then on
  requests to that domain are spaced to stay just under it. Episodes that
  share a domain share its allowance. If a `429` has no `Retry-After`, the
  pause doubles with each refusal instead.

Raising `--jobs` helps until the episodes in flight start sharing domains or
your connection is full. `--debug` prints each domain's measured limit and
pace.

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
- Several episodes are downloaded at once, and requests are paced per CDN
  domain. See [Speed](#speed).
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
internal/tui          the interactive search and settings questions
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
