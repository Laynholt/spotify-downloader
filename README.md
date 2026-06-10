# SpotiDownloader

Desktop Spotify downloader built with Wails, Go, React, and TypeScript.

The app fetches Spotify metadata, downloads audio through third-party sources, embeds tags/covers/lyrics, validates downloaded track duration, and can retry suspicious or failed tracks through `yt-dlp`.

![Windows](https://img.shields.io/badge/Windows-10%2B-0078D6?style=for-the-badge)
![Wails](https://img.shields.io/badge/Wails-v2-FF4B4B?style=for-the-badge)
![Go](https://img.shields.io/badge/Go-1.23%2B-00ADD8?style=for-the-badge)
![React](https://img.shields.io/badge/React-19-61DAFB?style=for-the-badge)

## Features

- Fetch tracks, albums, playlists, and artist releases from Spotify URLs.
- Download audio in MP3, FLAC, or M4A depending on settings.
- Embed Spotify metadata, cover art, MusicBrainz metadata, genres, and lyrics.
- Detect suspicious downloads by comparing Spotify duration with the downloaded file duration.
- Move suspicious source downloads into a `Suspicious` subfolder.
- Retry suspicious tracks, and optionally failed tracks, through YouTube using `yt-dlp`.
- Show a batch summary with downloaded, skipped, failed, duplicate, suspicious, and YouTube retry results.
- Update metadata for already downloaded files.
- Generate M3U8 playlists when enabled.
- Manage download history, queue state, file tools, audio conversion, and settings from the desktop UI.

## Download

Prebuilt releases are published on GitHub:

[Download SpotiDownloader](https://github.com/Laynholt/spotify-downloader/releases)

## Requirements

For running a release build:

- Windows 10 or newer.
- Internet access.
- FFmpeg for some audio operations. The app can help download it from settings.

For development:

- Go 1.23 or newer.
- Node.js and `pnpm`.
- Wails CLI v2.
- Python 3 if rebuilding the embedded token helper.
- UPX is optional; `build.ps1` uses it when available.

## Build

Install frontend dependencies once:

```powershell
cd frontend
pnpm install
```

Build the Windows application:

```powershell
cd ..
.\build.ps1
```

Reuse the existing embedded token helper and rebuild only the app:

```powershell
.\build.ps1 -NoHelperRebuild
```

The executable is written to:

```text
build/bin/SpotiDownloader.exe
```

## Test

Run backend tests:

```powershell
go test ./...
```

Run the frontend production build:

```powershell
cd frontend
pnpm run build
```

## Repository Notes

- `build/`, frontend build output, local caches, virtual environments, logs, and generated Wails bindings are ignored.
- `docs/superpowers/` and local `superpowers/` planning artifacts are ignored and should not be committed.
- `helper/uBOLite/` is intentionally kept because the Python token helper embeds it when rebuilt.
- `backend/bin/get_token.exe` is intentionally kept for Windows builds that use `-NoHelperRebuild`.

## FAQ

### Is this software free?

Yes. The software is free to use.

### Does this connect to my Spotify account?

No. The app fetches public Spotify metadata and does not require Spotify login.

### Where does the audio come from?

Audio is fetched through third-party sources. YouTube fallback downloads use `yt-dlp`.

### Why are some tracks marked suspicious?

A track is marked suspicious when the downloaded audio duration differs from Spotify's expected duration by more than the configured tolerance. This usually means the download source returned a different track.

### Why does Windows Defender or antivirus flag the executable?

This can happen with compressed unsigned executables, especially when UPX is used. If you are concerned, build the app locally from source.

## Credits

- [MusicBrainz](https://musicbrainz.org)
- [SpotiDownloader](https://spotidownloader.com)
- [Spotify Lyrics API](https://github.akashrchandran.in/spotify-lyrics-api)
- [LRCLIB](https://lrclib.net)
- [yt-dlp](https://github.com/yt-dlp/yt-dlp)

## Authors

- [Laynholt](https://github.com/Laynholt) - current maintainer.
- [afkarxyz](https://github.com/afkarxyz/) - original project author.

## Disclaimer

This project is for educational and private use only. The developer does not condone or encourage copyright infringement.

SpotiDownloader is a third-party tool and is not affiliated with, endorsed by, or connected to Spotify or any other streaming service.

You are solely responsible for ensuring your use of this software complies with your local laws and the terms of service of the respective platforms.

The software is provided "as is", without warranty of any kind. The author assumes no liability for any bans, damages, or legal issues arising from its use.
