# Third-party notices

PicDup bundles or links the following third-party components. Their licenses
apply to those components. The PicDup application code itself is MIT licensed;
see `LICENSE`.

## Wails v3

- Project: https://github.com/wailsapp/wails
- License: MIT
- Included as a Go module and linked into the desktop binary.

## golang.org/x/image

- Project: https://golang.org/x/image
- License: BSD 3-Clause
- Used for WebP, TIFF and BMP decoding, and high-quality image scaling.

## golang.org/x/sys

- Project: https://golang.org/x/sys
- License: BSD 3-Clause
- Used for Windows Recycle Bin / file attribute syscalls.

## Inter font

- Project: https://rsms.me/inter/
- Copyright 2020 The Inter Project Authors
- License: SIL Open Font License 1.1
- Bundled at `frontend/Inter-Medium.ttf`; the full license text is in
  `frontend/Inter-LICENSE.txt`.

## Other Go module dependencies

Wails' transitive dependencies include (among others) `github.com/adrg/xdg`,
`github.com/coder/websocket`, `github.com/go-ole/go-ole`,
`github.com/godbus/dbus/v5`, `github.com/mattn/go-colorable` and
`github.com/mattn/go-isatty`. These are distributed under their respective
MIT/BSD/Apache licenses. See the Go module cache or each project's repository
for the full text.
