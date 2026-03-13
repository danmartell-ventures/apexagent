package menubar

// Template icons for macOS menubar (16x16 PNG).
// These are minimal circle dot icons. In production they'd be proper assets,
// but for now we embed simple generated PNGs.
//
// macOS template images: only alpha channel matters (black/transparent).
// The system applies the correct color. So for template mode, we use a single icon.
// For non-template colored dots, we use separate green/yellow/red icons.

import _ "embed"

//go:embed icons/green.png
var iconGreen []byte

//go:embed icons/yellow.png
var iconYellow []byte

//go:embed icons/red.png
var iconRed []byte

//go:embed icons/gray.png
var iconGray []byte
