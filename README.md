# ektara

A terminal music player. It plays local mp3s and YouTube streams, and shows what
you're listening to as a Discord rich presence.

## What it does

- plays `.mp3` files out of a folder on your disk
- searches YouTube and downloads what it finds
- streams from YouTube without downloading anything
- queues several tracks together, from any mix of those sources
- keeps a history of what you played, in a local SQLite database
- shows the current track in your Discord status

## Requirements

- Go 1.27 or newer
- `ffmpeg`, **which you have to install yourself** — needed for all playback.
  Local files as much as streams, because the audio device is locked to a single
  sample rate for the whole session and ffmpeg is what converts to it. YouTube
  serves Opus and AAC, never mp3, so a stream could not be decoded without it
  anyway.

`ffprobe` ships inside ffmpeg, so it comes with the same install. It is used to
find out how long a local file is.

`yt-dlp` is downloaded and installed automatically the first time it is needed,
so there is nothing to do about that one.

## Build and run

```sh
go build ./...
```

Then run it from a folder that contains the music you want:

```sh
./ektara
```

 **it plays the mp3s in the current directory**, and it keeps its database there too. Running it 
 somewhere else means a different library and an empty history.

## The five modes

The menu asks which of these you want:

| mode | what it does |
| --- | --- |
| 1 | search YouTube, download the result, play the downloaded file |
| 2 | play an mp3 from the current directory |
| 3 | browse your history and play something from it |
| 4 | stream from YouTube, downloading nothing to disk |
| 5 | build a queue out of several tracks, which can mix all of the above |

Modes 1 to 4 hand back a single track. Only mode 5 gives you more than one, so
`a` and `d` only move between tracks when you are in a queue.

## Keys

While a track is playing:

| key | what it does |
| --- | --- |
| `p` | pause and unpause |
| `h` / `k` | seek back / forward five seconds |
| `a` / `d` | previous / next track |
| `s` | shuffle the queue |
| `r` | repeat the queue |
| `m` | mute |
| `+` / `-` | volume |
| `q` | back to the menu |

Shuffle and repeat are keys rather than menu settings because they are nice to
flip in the middle of listening. They hold for the rest of the session, so a
queue you shuffled stays shuffled next time.

While browsing history, the keys are different:

| key | what it does |
| --- | --- |
| `a` / `d` | older / newer |
| `enter` | play it |
| `q` or `esc` | cancel |

## What gets written to disk

Everything lands in the directory you ran it from:

- `songs.db` — your history, plus cover art URLs so repeat plays of the same
  track don't look them up again
- any mp3s you chose to download


## Known limits

**A stream that seeks will stall for a second or two.** YouTube's media urls
carry a token that expires, so seeking backwards on a stream has to kill ffmpeg
and resolve a fresh url before it can carry on. A local file has no url to renew,
so it only pays for the new ffmpeg, which is a few hundred milliseconds.

## Notes

The audio device is opened once, at one sample rate, and cannot be reopened
mid-track. Everything is resampled to that rate by ffmpeg as it plays, so a file
recorded at 44.1kHz sounds right on a 48kHz device. Nothing is decoded in
process.

The player display repaints a whole frame every 100ms and on every key press,
built as one string and written in one go, because a frame assembled from
separate writes shows half of each frame while it is being drawn.

A rewrite of the interface onto [Bubble Tea](https://charm.land/bubbletea/v2)
and [Lip Gloss](https://charm.land/lipgloss/v2) is in progress.
